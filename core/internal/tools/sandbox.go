package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// The sandbox is the second layer of workspace mode.
//
// The first layer is judgement (guard.go): deciding whether to ask. Judgement can always be fooled —
// computed paths, eval, dynamic loading, `curl | sh` — none of it visible to literal scanning. The
// second layer belongs to the kernel: under bwrap the outside is read-only, and writes simply fail.
//
// Only commands approved without asking are wrapped. A command that was asked about has been read and
// agreed to by the user, and wrapping it would stop them running what they typed.

var (
	bwrapOnce sync.Once
	bwrapPath string
	bwrapOK   bool
)

// SandboxAvailable reports whether this machine can sandbox.
//
// Only actually starting one counts: `bwrap --version` succeeding does not mean user namespaces can be
// created (some distributions disable them). When it cannot start, only the judgement layer remains —
// the previous release's behaviour, and losing a layer beats commands not running at all.
//
// Callers (the settings page) ask this too: on a machine without a sandbox, "inside the working
// directory" is only a literal check and the UI must stop claiming commands cannot write outside.
func SandboxAvailable() bool {
	bwrapOnce.Do(func() {
		path, err := exec.LookPath("bwrap")
		if err != nil {
			return
		}
		probe := exec.Command(path, "--ro-bind", "/", "/", "--dev", "/dev", "true")
		if probe.Run() != nil {
			return
		}
		bwrapPath = path
		bwrapOK = true
	})
	return bwrapOK
}

// writableCacheDirs are the exceptions that stay writable outside the directory.
//
// Without them, `go build`, `npm install` and `cargo build` die on "cache is not writable". The cost
// has to be stated: these paths can hold temporary files, so writing outside is not "impossible" but
// "only in these few places".
func writableCacheDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	candidates := []string{
		filepath.Join(home, ".cache"),
		filepath.Join(home, ".npm"),
		filepath.Join(home, ".cargo"),
		filepath.Join(home, ".bun"),
		filepath.Join(home, "go"),
		filepath.Join(home, ".local", "share", "pnpm"),
		filepath.Join(home, ".local", "share", "uv"),
	}
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			out = append(out, c)
		}
	}
	return out
}

// sandboxArgv wraps a command in bwrap.
//
// The boundary:
//
//	working directory  readable and writable (the agent's main job)
//	/tmp               readable and writable (without it a pile of tools fail mysteriously)
//	cache directories  readable and writable (nothing builds without them)
//	everything else    read-only
//
// The network is not isolated: `go mod download`, `npm install` and `curl` all need it, and blocking
// exfiltration needs a conversation of its own.
func sandboxArgv(root, cmdText string) []string {
	argv := []string{
		bwrapPath,
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--bind", "/tmp", "/tmp",
		"--bind", root, root,
		"--chdir", root,
		"--die-with-parent",
	}
	for _, dir := range writableCacheDirs() {
		argv = append(argv, "--bind", dir, dir)
	}
	return append(argv, "sh", "-c", cmdText)
}
