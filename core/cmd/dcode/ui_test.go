package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The cache is one directory per build; leaving the old ones behind costs ~25 MB each.
func TestPruneUICacheKeepsOnlyTheCurrentBuild(t *testing.T) {
	cache := t.TempDir()
	root := filepath.Join(cache, "dcode", "ui")
	for _, name := range []string{"0.1.0+git7.aaaa", "0.1.0+git8.bbbb", "0.1.0+git9.cccc.tmp"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "0.1.0+git8.bbbb", ".complete"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stray file in the cache directory is not ours to sweep.
	if err := os.WriteFile(filepath.Join(root, "notes"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	pruneUICache(cache, "0.1.0+git8.bbbb")

	if _, err := os.Stat(filepath.Join(root, "0.1.0+git8.bbbb", ".complete")); err != nil {
		t.Fatalf("the current tree was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "0.1.0+git7.aaaa")); !os.IsNotExist(err) {
		t.Fatalf("the previous build survived (err = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(root, "0.1.0+git9.cccc.tmp")); err != nil {
		t.Fatalf("an in-progress extraction was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "notes")); err != nil {
		t.Fatalf("a plain file was removed: %v", err)
	}
}

// A first run has no cache directory at all; pruning must be a no-op, not a failure.
func TestPruneUICacheWithoutCacheDirectory(t *testing.T) {
	pruneUICache(t.TempDir(), "0.1.0")
}
