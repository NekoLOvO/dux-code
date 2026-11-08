package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileReplacesContentAndMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	if err := WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteFile(path, []byte("second"), 0o644); err != nil {
		t.Fatalf("second write: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "second" {
		t.Errorf("content = %q, want %q", data, "second")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("mode = %o, want 644", got)
	}
}

// The temporary must not outlive the write: a leftover on every save would fill the config directory.
func TestWriteFileLeavesNoTemporary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	for i := 0; i < 3; i++ {
		if err := WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want just state.json", names)
	}
}

// The directory sync runs after the rename, so its error is the one case where the file has already
// been replaced and the call still fails: the content is on the device, the name is not promised to
// survive a power cut. Callers log it; nothing is rolled back.
func TestWriteFileReportsADirectoryItCannotSync(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the mode bits this test relies on")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "state")
	// Write and search, no read: creating and renaming inside the directory still works, opening it
	// to sync it does not.
	if err := os.Mkdir(sub, 0o300); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Restoring the mode is what lets t.TempDir clean up; if it fails the cleanup fails loudly anyway.
	t.Cleanup(func() { _ = os.Chmod(sub, 0o700) })

	path := filepath.Join(sub, "state.json")
	if err := WriteFile(path, []byte("x"), 0o600); err == nil {
		t.Fatal("WriteFile succeeded on a directory it cannot open to sync")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back after the failed sync: %v", err)
	}
	if string(data) != "x" {
		t.Errorf("content = %q, want %q", data, "x")
	}
}
