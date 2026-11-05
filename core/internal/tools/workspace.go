package tools

import (
	"os"
	"path/filepath"
	"strings"
)

// shellSafePaths are device files that do not count as out of bounds in a command.
//
// Without this allowlist, `cmd 2>/dev/null` — written every day — would force a confirmation, and the
// user would get used to clicking allow, which is worse than not asking.
var shellSafePaths = map[string]bool{
	"/dev/null":   true,
	"/dev/stdout": true,
	"/dev/stderr": true,
	"/dev/tty":    true,
}

// privilegeEscalators are commands where a directory boundary stops meaning anything.
// They take no paths, so literal scanning sees nothing; these simply cannot be approved.
var privilegeEscalators = map[string]bool{
	"sudo": true,
	"doas": true,
	"su":   true,
}

// commandStaysInside judges whether a command literally only touches things under root.
//
// It splits the command into words, picks out path-looking ones (starting with / ~ $HOME, or containing
// a separator), resolves each and checks for escapes. Deliberately over-eager: a false positive costs
// one question, a false negative silently approves, and the prices are not symmetric.
//
// What it cannot catch (genuinely cannot, not "not yet"):
//   - paths only known at run time: `cd $(mktemp -d)`, `$(dirname $0)`
//   - indirect expansion: `$OLDPWD`, `$PWD`
//   - a program doing it itself (`python3 -c "open('/etc/passwd')"` is caught, but `python3 evil.py`
//     passes as long as evil.py is in the directory)
//
// So this is "ask less often", not "locked down".
func commandStaysInside(root, cmd string) bool {
	// First look for "target known only after running". That is not a wrong judgement but an impossible
	// one, so it goes to a human: workspace mode would rather ask once more.
	if hasUnknowablePath(cmd) {
		return false
	}

	words := shellWords(cmd)
	for i, w := range words {
		if privilegeEscalators[w] {
			return false
		}
		// `cd -` goes back to $OLDPWD: literally just a minus, and where it leads is invisible.
		if w == "cd" && i+1 < len(words) && words[i+1] == "-" {
			return false
		}
		p, ok := pathCandidate(w)
		if !ok {
			continue
		}
		if !within(root, resolveForCheck(root, p)) {
			return false
		}
	}
	return true
}

// shellWords splits a command into words: whitespace and shell separators are boundaries.
//
// Splitting rather than writing a full shell parser, because all that is needed is "is there a string
// containing an absolute path outside the directory". Coarser splitting only asks more, never misses.
func shellWords(cmd string) []string {
	out := make([]string, 0, 16)
	var cur strings.Builder

	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}

	for _, r := range cmd {
		switch r {
		case ' ', '\t', '\n', '\r',
			';', '|', '&', '<', '>', '(', ')',
			'`', '\'', '"', '=', '\\':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// pathCandidate decides whether a word denotes a path, returning it unresolved when it does.
func pathCandidate(w string) (string, bool) {
	if w == "" {
		return "", false
	}
	// A URL is not a path (`//` in `https://x/y` would look like the root directory).
	if strings.Contains(w, "://") {
		return "", false
	}
	if strings.HasPrefix(w, "~") || strings.HasPrefix(w, "$HOME") || strings.HasPrefix(w, "${HOME}") {
		return w, true
	}
	if strings.HasPrefix(w, "/") {
		if shellSafePaths[w] {
			return "", false
		}
		return w, true
	}
	// A bare `..` is a path (no separator), and after `cd ..` everything else lands one level up. Missing
	// it would leave workspace mode with a back door.
	if w == ".." {
		return w, true
	}
	// Relative paths only count when they contain a separator (`src/x.go`); a bare name (`build`) cannot
	// escape root, so it needs no check.
	if strings.Contains(w, "/") {
		return w, true
	}
	return "", false
}

// resolveForCheck turns a candidate into an absolute, best-effort symlink-resolved real path.
func resolveForCheck(root, p string) string {
	home, _ := os.UserHomeDir()
	switch {
	case strings.HasPrefix(p, "${HOME}"):
		p = home + strings.TrimPrefix(p, "${HOME}")
	case strings.HasPrefix(p, "$HOME"):
		p = home + strings.TrimPrefix(p, "$HOME")
	case strings.HasPrefix(p, "~"):
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	return realPathBestEffort(filepath.Clean(p))
}

// hasUnknowablePath finds spellings whose target is only known at run time.
//
// Literal scanning only sees what is written. `rm -rf $DIR`, `cat $(pwd)/x.pp`, `mv a `date` b` show
// nothing at scan time and are exactly the dangerous ones, so they never pass in workspace mode.
//
// $HOME and ~ are the exception: their values are known, so they can really be resolved and checked.
func hasUnknowablePath(cmd string) bool {
	if strings.Contains(cmd, "`") || strings.Contains(cmd, "$(") {
		// Command substitution: anything can be inside, including a path.
		return true
	}

	for i := 0; i < len(cmd); i++ {
		switch cmd[i] {
		case '$':
			rest := cmd[i+1:]
			if strings.HasPrefix(rest, "${HOME}") {
				i += len("${HOME}")
				continue
			}
			if strings.HasPrefix(rest, "{HOME}") {
				i += len("{HOME}")
				continue
			}
			if strings.HasPrefix(rest, "HOME") && (len(rest) == 4 || !isIdentByte(rest[4])) {
				i += len("HOME")
				continue
			}
			// $VAR, ${VAR}, $1, $? …: an unknown value means an unknown target.
			return true
		case '~':
			// `~/x` is our own home (resolvable); `~user/x` is someone else's, and is not resolved.
			if i+1 < len(cmd) && isAlphaByte(cmd[i+1]) {
				return true
			}
		}
	}
	return false
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isAlphaByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
