// Package atomicfile writes a file by renaming a finished temporary over it.
//
// The daemon keeps its state in single JSON files (config, sessions, tasks) and rewrites the whole
// file on every change. A plain os.WriteFile truncates first, so a crash or a power cut between the
// truncate and the last write leaves a half-written file — and for the config that means losing every
// provider and key the user typed in. Writing to a temporary in the same directory and renaming makes
// the swap atomic: a reader either sees the old file or the new one, never the middle.
//
// The temporary is created in the target's directory on purpose: rename(2) is only atomic within one
// filesystem, and /tmp is usually a different one.
package atomicfile

import (
	"os"
	"path/filepath"
)

// WriteFile replaces path with data, mode included, atomically.
//
// The temporary is created with mode 0600 and loosened to mode afterwards, so the content is never
// readable by others while it is being written.
func WriteFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := fill(tmp, data, mode); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// fill writes, sets the mode on the open file (so the rename publishes it already correct) and closes.
func fill(tmp *os.File, data []byte, mode os.FileMode) error {
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	return tmp.Close()
}
