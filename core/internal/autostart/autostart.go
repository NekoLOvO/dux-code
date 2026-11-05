// Package autostart handles launch-at-login: a .desktop file under ~/.config/autostart,
// where the switch is simply whether the file exists.
//
// No resident service and no systemd unit of our own: XDG autostart is the convention every
// desktop honours, and systems with a systemd generator (Arch, Fedora) turn it into a user unit
// (app-com.nekolovo.dcode@autostart.service) while Hyprland + uwsm reads it directly. A
// hand-written unit would cover only half of the desktops.
package autostart

import (
	"os"
	"path/filepath"
	"strings"
)

// FileName is the file inside the autostart directory. It matches AppId so the unit name the
// systemd generator derives (app-<file>@autostart.service) lines up.
const FileName = "com.nekolovo.dcode.desktop"

// Dir is the autostart directory.
func Dir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "autostart")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "autostart")
}

// Path is the full path of our .desktop file.
func Path() string { return filepath.Join(Dir(), FileName) }

// Enabled only checks whether the file exists: deleting it by hand turns the switch off.
func Enabled() bool {
	_, err := os.Stat(Path())
	return err == nil
}

// Set turns it on or off. exe is dcode's own absolute path: PATH at login need not match the
// user's terminal, so an absolute path is the safe choice.
//
// --hidden is passed by default — this is "start in the background at login", and nobody wants a
// large window on boot. Use dcode show (IPC, raises the running instance) or the tray.
func Set(enabled bool, exe string) error {
	path := Path()
	if !enabled {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(desktopEntry(exe)), 0o644)
}

func desktopEntry(exe string) string {
	var b strings.Builder
	b.WriteString("[Desktop Entry]\n")
	b.WriteString("Type=Application\n")
	b.WriteString("Name=Dux Code\n")
	b.WriteString("Comment=Dux Code background service\n")
	b.WriteString("Exec=" + quoteExec(exe) + " ui --hidden\n")
	b.WriteString("Icon=com.nekolovo.dcode\n")
	b.WriteString("Terminal=false\n")
	b.WriteString("X-GNOME-Autostart-enabled=true\n")
	return b.String()
}

// quoteExec applies the Desktop Entry rules: quoting is only needed for spaces or quotes, and
// once quoted the characters " ` $ \\ must be escaped or Exec is split wrong.
func quoteExec(p string) string {
	if !strings.ContainsAny(p, " \t\"'`$\\") {
		return p
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`)
	return `"` + r.Replace(p) + `"`
}
