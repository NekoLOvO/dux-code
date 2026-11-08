package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/NekoLOvO/dux-code/core/internal/uibundle"
)

var (
	uiConfigFlag string
	uiHidden     bool
)

var uiCmd = &cobra.Command{
	Use:   "ui",
	Short: "Open the UI (starting the daemon if needed)",
	Long: `Start Quickshell with this QML tree, bringing the daemon up first if needed.

The QML comes from the first of these that exists:
  -c, --config <dir>       an explicit tree; the usual choice during development
  embedded copy            packaged builds only; extracted to ~/.cache/dcode/ui/<version>
  $DUXCODE_UI_DIR          taken from the environment
  next to the binary       <prefix>/bin/dcode → <prefix>/share/dcode/ui

Embedding the UI keeps an installation to a single file: one Exec=dcode ui
line in the desktop entry starts both the daemon and the window, which is
exactly what autostart needs.

Only one instance runs: launching again shows the window that already exists
instead of opening a second one (and with --hidden it simply exits).
DUXCODE_ENABLE_HOTRELOAD=1 is the exception, since that is the development
flow and needs a fresh shell.`,
	SilenceUsage: true,
	RunE: func(_ *cobra.Command, _ []string) error {
		dir, err := resolveUIDir(uiConfigFlag)
		if err != nil {
			return err
		}
		quickshell, err := exec.LookPath("quickshell")
		if err != nil {
			return errors.New("quickshell not found: the UI is drawn by Quickshell. Install quickshell 0.3 or newer and try again")
		}
		if err := ensureDaemon(); err != nil {
			return err
		}
		// One instance only: a second launch is a request to show the window that already exists, not a
		// second window (two Quickshells would both connect and both draw the same sessions). Autostart
		// at login must not pop a window either, so --hidden just exits.
		//
		// Hot reload is the exception: `make hot` is a deliberate "start a fresh shell on this tree",
		// and answering it by raising the old window would silently give the developer stale QML.
		if pid, running := runningShellPID(); running && !hotReloadEnabled() {
			if uiHidden {
				return nil
			}
			if err := callShell(pid, "raise"); err != nil {
				// The usual cause is an instance started before the handler was renamed; it cannot be
				// raised, so say what to do instead of leaving the user with a bare "function not found".
				return fmt.Errorf("cannot raise the running window (%w); close it and start again", err)
			}
			return nil
		}
		if uiHidden {
			// shell.qml reads this variable: tray only, no window.
			_ = os.Setenv("DUXCODE_START_HIDDEN", "1")
		}
		// exec rather than spawn: the window's parent becomes quickshell itself, so closing the window
		// leaves no idle dcode behind and signals reach it directly.
		return syscall.Exec(quickshell, []string{quickshell, "-p", filepath.Join(dir, "shell.qml")}, os.Environ())
	},
}

func init() {
	uiCmd.Flags().StringVarP(&uiConfigFlag, "config", "c", "", "UI directory (must contain shell.qml)")
	uiCmd.Flags().BoolVar(&uiHidden, "hidden", false, "Start with no window, system tray only (for login autostart)")
	rootCmd.AddCommand(uiCmd)
}

// hotReloadEnabled reports whether this launch is the development flow (`make hot`), which is
// allowed to start a second instance.
func hotReloadEnabled() bool {
	switch os.Getenv("DUXCODE_ENABLE_HOTRELOAD") {
	case "1", "true":
		return true
	}
	return false
}

// resolveUIDir decides which QML tree to run.
func resolveUIDir(dir string) (string, error) {
	if dir != "" {
		return requireShellQML(dir)
	}
	if uibundle.FS != nil {
		return extractUI()
	}
	if env := os.Getenv("DUXCODE_UI_DIR"); env != "" {
		return requireShellQML(env)
	}
	if exe, err := os.Executable(); err == nil {
		base := filepath.Dir(exe)
		// An installed layout is <prefix>/bin/dcode plus <prefix>/share/dcode/ui.
		for _, cand := range []string{
			filepath.Join(base, "..", "share", "dcode", "ui"),
			filepath.Join(base, "ui"),
		} {
			if p, err := requireShellQML(cand); err == nil {
				return p, nil
			}
		}
	}
	for _, cand := range []string{"/usr/share/dcode/ui", "/usr/local/share/dcode/ui"} {
		if p, err := requireShellQML(cand); err == nil {
			return p, nil
		}
	}
	return "", errors.New("this dcode has no embedded UI and no UI directory was found: pass -c <dir>, or use a packaged build")
}

func requireShellQML(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(filepath.Join(abs, "shell.qml")); err != nil || st.IsDir() {
		return "", fmt.Errorf("%s has no shell.qml, so it is not a UI directory", abs)
	}
	return abs, nil
}

// extractUI unpacks the embedded QML into the cache directory.
//
// Quickshell loads from the filesystem, so the tree cannot be fed from memory — it has to become
// a real path. The directory name carries the version, so a version switch switches directories
// and never mixes old and new files.
func extractUI() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	// The name has to include what was actually built, not just the version: two builds of one
	// commit (or of a dirty tree) share a version string and would reuse a stale cache.
	key := uiCacheKey()
	target := filepath.Join(cache, "dcode", "ui", key)
	done := filepath.Join(target, ".complete")
	if _, err := os.Stat(done); err == nil {
		pruneUICache(cache, key)
		return target, nil
	}

	// Write alongside, then rename: an interruption leaves no half-complete directory that looks
	// valid.
	tmp := target + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := writeFS(uibundle.FS, tmp); err != nil {
		return "", fmt.Errorf("failed to extract the embedded UI: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".complete"), []byte(Version+"\n"), 0o644); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		// Another process (or a leftover from a previous run) got there first.
		if _, statErr := os.Stat(done); statErr == nil {
			_ = os.RemoveAll(tmp)
			pruneUICache(cache, key)
			return target, nil
		}
		_ = os.RemoveAll(target)
		if err2 := os.Rename(tmp, target); err2 != nil {
			return "", err2
		}
	}
	pruneUICache(cache, key)
	return target, nil
}

// pruneUICache deletes extracted UI trees other than the one just resolved.
//
// Every build produces a new cache key (version + build time), so without this each upgrade leaves
// a full ~25 MB tree behind forever. The tree is derived data — deleting one only costs a single
// extraction if that build runs again — and this only happens when no shell of ours is up, since
// `dcode ui` raises the running window instead of extracting.
//
// Entries ending in .tmp are another process's half-written tree, and anything that is not a
// directory is left alone: the cache directory is not ours to sweep.
func pruneUICache(cache, keep string) {
	root := filepath.Join(cache, "dcode", "ui")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || name == keep || strings.HasSuffix(name, ".tmp") {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, name))
	}
}

// uiCacheKey names the cache directory: version plus build time. BuildTime differs on every
// build, so changed content always gets a new directory.
func uiCacheKey() string {
	stamp := strings.Map(func(r rune) rune {
		if r == ':' || r == '-' || r == '+' {
			return -1
		}
		return r
	}, BuildTime)
	return Version + "-" + stamp
}

func writeFS(fsys fs.FS, dest string) error {
	return fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out := filepath.Join(dest, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		mode := os.FileMode(0o644)
		if info, ierr := d.Info(); ierr == nil && info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		src, err := fsys.Open(p)
		if err != nil {
			return err
		}
		// Only read from; the write side below is the one whose close can lose data.
		defer func() { _ = src.Close() }()
		dst, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(dst, src); err != nil {
			_ = dst.Close()
			return err
		}
		return dst.Close()
	})
}

// ensureDaemon checks that something listens on the socket and starts a daemon if not.
//
// `make run` does the same, but that is the development path; after installation nothing else
// starts the daemon, so this has to stand on its own — otherwise autostart opens a window that
// cannot reach a backend.
func ensureDaemon() error {
	sock := socketPath()
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	if daemonAlive(sock) {
		// The daemon is setsid'd, so closing the UI does not take it along. A replaced binary (rebuild,
		// upgrade) still talking to the old daemon is the classic "changed Go, seeing old behaviour".
		if !binaryNewer(exe, sock) {
			return nil
		}
		stopStaleDaemon(sock)
	}
	stateDir := filepath.Join(xdgDir("XDG_STATE_HOME", ".local", "state"), "dcode")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(stateDir, "daemon.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	// The child writes into this file, so it has to outlive this function; a failed close on an
	// append-only log has nothing to lose and nowhere to be reported.
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(exe, "daemon")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Closing the window must not take the backend with it: the daemon lives on and the next UI
	// connects straight back.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start the daemon: %w", err)
	}
	_ = cmd.Process.Release()

	for i := 0; i < 60; i++ {
		if _, err := os.Stat(sock); err == nil {
			if conn, derr := net.Dial("unix", sock); derr == nil {
				// The dial is the probe; hanging up on it says nothing either way.
				_ = conn.Close()
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for the daemon socket (%s); see %s/daemon.log", sock, stateDir)
}

func daemonAlive(sock string) bool {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return false
	}
	// Dialling is the whole question; a close error would only mean the socket went away in between.
	_ = conn.Close()
	return true
}

// binaryNewer compares the executable's mtime with the socket's: the socket is created when the
// daemon binds, so an older socket means the daemon is running an older build.
func binaryNewer(exe, sock string) bool {
	bin, err := os.Stat(exe)
	if err != nil {
		return false
	}
	st, err := os.Stat(sock)
	if err != nil {
		return false
	}
	return bin.ModTime().After(st.ModTime())
}

// daemonPid reads the pid from the handshake line. No /proc scan and no ss: those are often
// missing in containers and minimal systems.
func daemonPid(sock string) int {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return 0
	}
	// Reads only the greeting line; whatever the close reports afterwards is not about the version.
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return 0
	}
	var hello struct {
		PID int `json:"pid"`
	}
	if json.Unmarshal(line, &hello) != nil {
		return 0
	}
	return hello.PID
}

func stopStaleDaemon(sock string) {
	pid := daemonPid(sock)
	if pid <= 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "dcode: the running daemon (pid %d) is older than this binary, restarting\n", pid)
	_ = syscall.Kill(pid, syscall.SIGTERM)
	for i := 0; i < 100; i++ {
		if !daemonAlive(sock) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "dcode: the old daemon (pid %d) did not exit within 5s; kill it and retry\n", pid)
}

func xdgDir(env, fallbackA, fallbackB string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallbackA, fallbackB)
}
