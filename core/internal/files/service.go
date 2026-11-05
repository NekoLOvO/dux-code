// Package files is the backend for the component library's file browser
// (DCommon/FileBrowser).
//
// Only the subset needed to pick a folder: userDirs / watch / list / unwatch / count. Response
// shapes match dux-go/files (entry fields, {entries,total,hidden,cursor}) so DirectoryModel.qml
// needs no changes.
package files

import (
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/NekoLOvO/dux-code/core/internal/ipc"
)

// Service holds the watch state. A watchId is only valid inside one daemon process.
type Service struct {
	mu      sync.Mutex
	seq     int
	watches map[string]*watchEntry
}

type watchEntry struct {
	path string
	opts listOptions
	at   time.Time
}

type listOptions struct {
	includeHidden bool
	sortKey       string
	desc          bool
	dirsFirst     bool
	limit         int
	filters       []string
}

// Entry's fields must line up with dux-go/files: FileRow.qml declares most of them as required
// properties, and a missing one is "Cannot create delegate" with an empty list.
type Entry struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	IsDir         bool   `json:"isDir"`
	IsSymlink     bool   `json:"isSymlink"`
	SymlinkTarget string `json:"symlinkTarget"`
	SymlinkBroken bool   `json:"symlinkBroken"`
	Size          int64  `json:"size"`
	MtimeMs       int64  `json:"mtimeMs"`
	CtimeMs       int64  `json:"ctimeMs"`
	AtimeMs       int64  `json:"atimeMs"`
	Mode          string `json:"mode"`
	Owner         string `json:"owner"`
	Group         string `json:"group"`
	Hidden        bool   `json:"hidden"`
	Executable    bool   `json:"isExecutable"`
	Extension     string `json:"extension"`
	Mime          string `json:"mime"`
	IconName      string `json:"iconName"`
	Thumbnail     string `json:"thumbnail"`
	Thumbnailable bool   `json:"thumbnailable"`
	Unreadable    bool   `json:"unreadable"`
	DisplayName   string `json:"displayName"`
	Untrusted     bool   `json:"untrusted"`
}

func New() *Service {
	return &Service{watches: map[string]*watchEntry{}}
}

// Handle is called by api.route for methods prefixed with files.
func (s *Service) Handle(w *ipc.Writer, req ipc.Request) {
	switch req.Method {
	case "files.userDirs":
		w.Respond(req.ID, map[string]any{"dirs": userDirs()})
	case "files.watch":
		s.watch(w, req)
	case "files.list":
		s.list(w, req)
	case "files.unwatch":
		s.unwatch(w, req)
	case "files.count":
		s.count(w, req)
	default:
		// thumbnail / icon / size / trash…: unused when picking a folder, so answer with a real
		// error instead of pretending.
		w.RespondError(req.ID, "unsupported method: "+req.Method)
	}
}

func (s *Service) watch(w *ipc.Writer, req ipc.Request) {
	path, _ := req.Params["path"].(string)
	if path == "" {
		w.RespondError(req.ID, "path is required")
		return
	}
	opts := parseOptions(req.Params)

	entries, hidden, err := readDir(path, opts)
	if err != nil {
		w.RespondError(req.ID, err.Error())
		return
	}

	s.mu.Lock()
	s.seq++
	id := "w" + strconv.Itoa(s.seq)
	s.watches[id] = &watchEntry{path: path, opts: opts, at: time.Now()}
	s.mu.Unlock()

	// watching=false: there is no inotify stream; pollOnFocus relists instead.
	page, cursor := paginate(entries, "", opts.limit)
	w.Respond(req.ID, map[string]any{
		"watchId":     id,
		"watching":    false,
		"pollOnFocus": true,
		"entries":     page,
		"cursor":      cursor,
		"total":       len(entries),
		"hidden":      hidden,
	})
}

func (s *Service) list(w *ipc.Writer, req ipc.Request) {
	id, _ := req.Params["watchId"].(string)

	s.mu.Lock()
	we := s.watches[id]
	s.mu.Unlock()
	if we == nil {
		w.RespondError(req.ID, "unknown watchId")
		return
	}

	entries, hidden, err := readDir(we.path, we.opts)
	if err != nil {
		w.RespondError(req.ID, err.Error())
		return
	}
	cursor, _ := req.Params["cursor"].(string)
	page, next := paginate(entries, cursor, we.opts.limit)
	w.Respond(req.ID, map[string]any{
		"entries": page,
		"cursor":  next,
		"total":   len(entries),
		"hidden":  hidden,
		"seq":     len(entries),
	})
}

func (s *Service) unwatch(w *ipc.Writer, req ipc.Request) {
	id, _ := req.Params["watchId"].(string)
	s.mu.Lock()
	delete(s.watches, id)
	s.mu.Unlock()
	w.Respond(req.ID, map[string]any{"ok": true})
}

// count returns {counts: {path: n}}, which FileViewBody shows as "N items".
func (s *Service) count(w *ipc.Writer, req ipc.Request) {
	paths := stringSlice(req.Params["paths"])
	hidden := req.Params["includeHidden"] == true
	counts := map[string]int{}
	for _, p := range paths {
		items, err := os.ReadDir(p)
		if err != nil {
			continue
		}
		n := 0
		for _, it := range items {
			if !hidden && strings.HasPrefix(it.Name(), ".") {
				continue
			}
			n++
		}
		counts[p] = n
	}
	w.Respond(req.ID, map[string]any{"counts": counts})
}

// --- listing ---

func readDir(path string, opts listOptions) ([]Entry, int, error) {
	items, err := os.ReadDir(path)
	if err != nil {
		return nil, 0, err
	}

	entries := make([]Entry, 0, len(items))
	hidden := 0
	for _, it := range items {
		name := it.Name()
		isDot := strings.HasPrefix(name, ".")
		if isDot && !opts.includeHidden {
			hidden++
			continue
		}
		info, err := it.Info()
		if err != nil {
			continue
		}
		e := buildEntry(path, name, isDot, info)
		// filters only select files; directories always pass (same as dux-go).
		if len(opts.filters) > 0 && !e.IsDir && !matchesFilters(name, opts.filters) {
			continue
		}
		entries = append(entries, e)
	}

	sortEntries(entries, opts)
	return entries, hidden, nil
}

var (
	uidNames = map[uint32]string{}
	gidNames = map[uint32]string{}
	nameMu   sync.Mutex
)

func buildEntry(dir, name string, hidden bool, info os.FileInfo) Entry {
	full := filepath.Join(dir, name)
	e := Entry{
		Name:        name,
		Path:        full,
		IsDir:       info.IsDir(),
		IsSymlink:   info.Mode()&os.ModeSymlink != 0,
		Size:        info.Size(),
		MtimeMs:     info.ModTime().UnixMilli(),
		Hidden:      hidden,
		DisplayName: name,
		Mode:        info.Mode().String(),
		Executable:  info.Mode()&0o111 != 0,
		IconName:    iconFor(name, info.IsDir()),
	}
	if !e.IsDir {
		e.Extension = strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	}
	if e.IsSymlink {
		if target, err := os.Readlink(full); err == nil {
			e.SymlinkTarget = target
		}
		if _, err := os.Stat(full); err != nil {
			e.SymlinkBroken = true
			e.Unreadable = true
		}
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		e.CtimeMs = st.Ctim.Sec*1000 + st.Ctim.Nsec/1e6
		e.AtimeMs = st.Atim.Sec*1000 + st.Atim.Nsec/1e6
		e.Owner = lookupUser(st.Uid)
		e.Group = lookupGroup(st.Gid)
	}
	return e
}

func lookupUser(uid uint32) string {
	nameMu.Lock()
	defer nameMu.Unlock()
	if n, ok := uidNames[uid]; ok {
		return n
	}
	name := strconv.FormatUint(uint64(uid), 10)
	if u, err := user.LookupId(name); err == nil {
		name = u.Username
	}
	uidNames[uid] = name
	return name
}

func lookupGroup(gid uint32) string {
	nameMu.Lock()
	defer nameMu.Unlock()
	if n, ok := gidNames[gid]; ok {
		return n
	}
	name := strconv.FormatUint(uint64(gid), 10)
	if g, err := user.LookupGroupId(name); err == nil {
		name = g.Name
	}
	gidNames[gid] = name
	return name
}

func sortEntries(entries []Entry, opts listOptions) {
	key := opts.sortKey
	if key == "" {
		key = "name"
	}
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if opts.dirsFirst && a.IsDir != b.IsDir {
			return a.IsDir
		}
		var less bool
		switch key {
		case "size":
			less = a.Size < b.Size
		case "mtime":
			less = a.MtimeMs < b.MtimeMs
		case "type":
			less = a.Extension < b.Extension
		default:
			less = strings.ToLower(a.Name) < strings.ToLower(b.Name)
		}
		if opts.desc {
			return !less
		}
		return less
	})
}

func paginate(entries []Entry, cursor string, limit int) ([]Entry, string) {
	if limit <= 0 {
		limit = 500
	}
	offset := 0
	if cursor != "" {
		if n, err := strconv.Atoi(cursor); err == nil && n > 0 {
			offset = n
		}
	}
	if offset >= len(entries) {
		return []Entry{}, ""
	}
	end := offset + limit
	next := ""
	if end < len(entries) {
		next = strconv.Itoa(end)
	} else {
		end = len(entries)
	}
	return entries[offset:end], next
}

func matchesFilters(name string, patterns []string) bool {
	lower := strings.ToLower(name)
	for _, p := range patterns {
		if ok, err := filepath.Match(strings.ToLower(p), lower); err == nil && ok {
			return true
		}
	}
	return false
}

func iconFor(name string, isDir bool) string {
	if isDir {
		return "folder"
	}
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(name), ".")) {
	case "md", "txt":
		return "description"
	case "go", "js", "ts", "py", "rs", "c", "h", "cpp", "sh":
		return "code"
	case "json", "yaml", "yml", "toml":
		return "data_object"
	case "png", "jpg", "jpeg", "webp", "gif", "svg":
		return "image"
	default:
		return "draft"
	}
}

// --- XDG user directories ---

type userDir struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	IconName string `json:"iconName"`
}

func userDirs() []userDir {
	home, _ := os.UserHomeDir()
	paths := userDirPaths(home)

	// Name is always the basename. XDG already names these directories in the user's language
	// (Desktop, Documents, Downloads…), so translating again only invents names — and it would
	// override a folder the user renamed. dfiles does the same (home shows the user name).
	out := []userDir{{Key: "home", Name: filepath.Base(home), Path: home, IconName: "user-home"}}
	order := []struct {
		env, key, icon string
	}{
		{"XDG_DESKTOP_DIR", "desktop", "user-desktop"},
		{"XDG_DOCUMENTS_DIR", "documents", "folder-documents"},
		{"XDG_DOWNLOAD_DIR", "download", "folder-download"},
		{"XDG_MUSIC_DIR", "music", "folder-music"},
		{"XDG_PICTURES_DIR", "pictures", "folder-pictures"},
		{"XDG_VIDEOS_DIR", "videos", "folder-videos"},
		{"XDG_PUBLICSHARE_DIR", "publicshare", "folder-publicshare"},
		{"XDG_TEMPLATES_DIR", "templates", "folder-templates"},
	}
	for _, d := range order {
		p := paths[d.env]
		if p == "" || p == home {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			continue
		}
		out = append(out, userDir{Key: d.key, Name: filepath.Base(p), Path: p, IconName: d.icon})
	}
	return out
}

// userDirPaths reads ~/.config/user-dirs.dirs (XDG_DESKTOP_DIR="…" lines).
// The environment wins because that is what the spec says; the file is just the usual source.
func userDirPaths(home string) map[string]string {
	paths := map[string]string{}
	for _, key := range []string{
		"XDG_DESKTOP_DIR", "XDG_DOCUMENTS_DIR", "XDG_DOWNLOAD_DIR", "XDG_MUSIC_DIR",
		"XDG_PICTURES_DIR", "XDG_VIDEOS_DIR", "XDG_PUBLICSHARE_DIR", "XDG_TEMPLATES_DIR",
	} {
		if v := os.Getenv(key); v != "" {
			paths[key] = filepath.Clean(v)
		}
	}

	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	raw, err := os.ReadFile(filepath.Join(configHome, "user-dirs.dirs"))
	if err != nil {
		return paths
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if _, seen := paths[k]; seen {
			continue // environment first
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		v = strings.ReplaceAll(v, "$HOME", home)
		if v == "" || !strings.HasPrefix(v, "/") {
			continue
		}
		paths[k] = filepath.Clean(v)
	}
	return paths
}

// --- argument parsing ---

func parseOptions(params map[string]any) listOptions {
	opts := listOptions{
		dirsFirst: true,
		sortKey:   "name",
		limit:     500,
	}
	if v, ok := params["includeHidden"].(bool); ok {
		opts.includeHidden = v
	}
	if v, ok := params["dirsFirst"].(bool); ok {
		opts.dirsFirst = v
	}
	if v, ok := params["desc"].(bool); ok {
		opts.desc = v
	}
	if v, ok := params["sort"].(string); ok && v != "" {
		opts.sortKey = v
	}
	if v, ok := params["limit"].(float64); ok && v > 0 {
		opts.limit = int(v)
	}
	opts.filters = stringSlice(params["filters"])
	return opts
}

func stringSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, it := range raw {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
