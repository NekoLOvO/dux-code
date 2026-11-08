package autostart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetWritesDesktopEntry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	if Enabled() {
		t.Fatal("reports enabled before anything was set")
	}
	if err := Set(true, "/usr/bin/dcode"); err != nil {
		t.Fatalf("Set(true): %v", err)
	}
	if !Enabled() {
		t.Fatal("wrote the file but reports disabled")
	}

	body, err := os.ReadFile(filepath.Join(dir, "autostart", FileName))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{
		"[Desktop Entry]",
		"Exec=/usr/bin/dcode ui --hidden",
		"X-GNOME-Autostart-enabled=true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q, actual content:\n%s", want, got)
		}
	}
}

func TestSetFalseRemovesEntry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	if err := Set(true, "/usr/bin/dcode"); err != nil {
		t.Fatal(err)
	}
	if err := Set(false, "/usr/bin/dcode"); err != nil {
		t.Fatalf("Set(false): %v", err)
	}
	if Enabled() {
		t.Fatal("reports enabled after being turned off")
	}
	// Disabling twice must not error (the file is already gone).
	if err := Set(false, "/usr/bin/dcode"); err != nil {
		t.Fatalf("second disable: %v", err)
	}
}

// A path with spaces must be quoted, or login splits it into two arguments and the launch
// fails.
func TestSetQuotesPathWithSpaces(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	if err := Set(true, "/home/some one/bin/dcode"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `Exec="/home/some one/bin/dcode" ui --hidden`) {
		t.Fatalf("not escaped correctly:\n%s", body)
	}
}
