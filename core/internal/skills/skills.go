// Package skills scans local agent skills.
//
// The location is the cross-tool convention `~/.agents/skills`:
//
//	~/.agents/skills/<name>/SKILL.md
//
// SKILL.md opens with YAML frontmatter (name / description / allowed-tools) followed by prose for
// the model. This package only reads the list; loading a skill into context is the agent's job.
package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Skill is one skill on disk.
type Skill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Path        string   `json:"path"` // absolute path of SKILL.md
	Dir         string   `json:"dir"`
	Tools       []string `json:"tools,omitempty"` // frontmatter allowed-tools
	// Warning flags a skill that has a problem but still exists. It does not go into Skipped:
	// one skill must not render as two rows.
	Warning string `json:"warning,omitempty"`
}

// Dir returns the skills root (which may not exist).
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".agents", "skills")
}

// Skipped is a directory that looks like a skill but is not.
//
// It exists for one reason: after dropping files into ~/.agents/skills, a user whose skill simply
// does not appear has nothing to go on. Naming the reason ends the guessing.
type Skipped struct {
	Dir    string `json:"dir"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Scan is one full scan.
type Scan struct {
	Dir     string    `json:"dir"`
	Skills  []Skill   `json:"skills"`
	Skipped []Skipped `json:"skipped"`
}

// ScanSkills scans the skills directory and reports the skipped ones too.
func ScanSkills() (Scan, error) {
	root := Dir()
	if root == "" {
		return Scan{}, nil
	}
	res := Scan{Dir: root}

	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil
		}
		return res, err
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())

		path, ok := skillFile(dir)
		if !ok {
			res.Skipped = append(res.Skipped, Skipped{
				Dir:    dir,
				Name:   e.Name(),
				Reason: "SKILL.md is missing",
			})
			continue
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			res.Skipped = append(res.Skipped, Skipped{
				Dir:    dir,
				Name:   e.Name(),
				Reason: "cannot read SKILL.md: " + err.Error(),
			})
			continue
		}

		s := parse(string(raw))
		s.Path = path
		s.Dir = dir
		if s.Name == "" {
			s.Name = e.Name()
		}
		if strings.TrimSpace(s.Description) == "" {
			// Not fatal, but worth saying: a skill without a description never reaches the system
			// prompt list, so the model never learns it exists. The warning rides on the skill
			// itself rather than in Skipped, which would show the same name twice.
			s.Warning = "SKILL.md has no description, so the model will not see it"
		}
		res.Skills = append(res.Skills, s)
	}

	sort.Slice(res.Skills, func(i, j int) bool { return res.Skills[i].Name < res.Skills[j].Name })
	sort.Slice(res.Skipped, func(i, j int) bool { return res.Skipped[i].Name < res.Skipped[j].Name })
	return res, nil
}

// skillFile finds SKILL.md in a folder.
//
// Case-insensitive: some tools write skill.md, and insisting on SKILL.md is just hostile.
func skillFile(dir string) (string, bool) {
	for _, name := range []string{"SKILL.md", "skill.md", "Skill.md"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

// List returns only the successful ones (for the system prompt and load_skill).
func List() ([]Skill, error) {
	res, err := ScanSkills()
	return res.Skills, err
}

// Load returns a skill's full prose (everything after the frontmatter).
//
// Both the frontmatter name and the folder name are accepted: the model only sees the name, but
// looking a skill up by folder is natural, and the two often differ (folder `my-skill`, name
// "My Skill").
func Load(name string) (Skill, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Skill{}, "", errors.New("the skill name is empty")
	}

	list, err := List()
	if err != nil {
		return Skill{}, "", err
	}
	for _, s := range list {
		if s.Name != name && filepath.Base(s.Dir) != name {
			continue
		}
		raw, err := os.ReadFile(s.Path)
		if err != nil {
			return Skill{}, "", err
		}
		return s, body(string(raw)), nil
	}

	names := make([]string, 0, len(list))
	for _, s := range list {
		names = append(names, s.Name)
	}
	if len(names) == 0 {
		return Skill{}, "", fmt.Errorf("no skill named %q: nothing is installed yet", name)
	}
	return Skill{}, "", fmt.Errorf("no skill named %q. Installed: %s", name, strings.Join(names, ", "))
}

// body strips the frontmatter, leaving the prose for the model.
func body(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(raw, "---") {
		return strings.TrimSpace(raw)
	}
	rest := raw[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return strings.TrimSpace(raw)
	}
	return strings.TrimSpace(rest[end+len("\n---"):])
}

// parse reads the frontmatter. Only the flat `key: value` form is supported: a skill's
// frontmatter is three or four lines, not worth a YAML dependency.
func parse(raw string) Skill {
	var s Skill
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(raw, "---") {
		return s
	}
	rest := raw[3:]
	// find the closing ---
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return s
	}
	head := rest[:end]

	for _, line := range strings.Split(head, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		switch key {
		case "name":
			s.Name = val
		case "description":
			s.Description = val
		case "allowed-tools", "allowed_tools", "tools":
			s.Tools = parseList(val)
		}
	}
	return s
}

// parseList handles both `["a", "b"]` and `a, b`.
func parseList(v string) []string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "[") {
		// The normal case is a JSON array; json handles quotes and commas correctly.
		var arr []string
		if err := json.Unmarshal([]byte(v), &arr); err == nil {
			out := make([]string, 0, len(arr))
			for _, s := range arr {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
			return out
		}
	}

	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		// Trim spaces before quotes, or a leading quote survives.
		p = strings.TrimSpace(p)
		p = strings.Trim(p, "\"'")
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
