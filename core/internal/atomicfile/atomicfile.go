// Package atomicfile writes a file by renaming a finished temporary over it.
//
// The daemon keeps its state in single JSON files (config, sessions, tasks) and rewrites the whole
// file on every change. A plain os.WriteFile truncates first, so a crash or a power cut between the
// truncate and the last write leaves a half-written file — and for the config that means losing every
// provider and key the user typed in. Writing to a temporary in the same directory and renaming makes
// the swap atomic: a reader either sees the old file or the new one, never the middle.
//
// Atomic is not the same as durable, and the difference cost a real session. A rename only publishes a
// name; the bytes behind it can still be sitting in the page cache, and on a filesystem that allocates
// data blocks lazily (f2fs does) the inode can reach the checkpoint with a full size and a block count
// while every one of those blocks is still unwritten. Cut the power there and the file survives with
// the right length and nothing but NUL bytes in it — JSON, reports as binary, unopenable by anything.
// So the data is synced before the rename, and the directory is synced after it: the first makes the
// content reach the device, the second makes the new name reach it.
//
// The temporary is created in the target's directory on purpose: rename(2) is only atomic within one
// filesystem, and /tmp is usually a different one.
package atomicfile

import (
	"os"
	"path/filepath"
)

// WriteFile replaces path with data, mode included, atomically and durably.
//
// The temporary is created with mode 0600 and loosened to mode afterwards, so the content is never
// readable by others while it is being written.
func WriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := fill(tmp, data, mode); err != nil {
		// The temporary is being thrown away, so a failure to remove it is not worth replacing the
		// real error with.
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	// The rename is only durable once the directory entry behind it is on the device too.
	return syncDir(dir)
}

// fill writes, sets the mode on the open file (so the rename publishes it already correct), syncs and
// closes. Sync is before the close for a reason: it is the only thing that says the bytes are on the
// device rather than in the page cache.
//
// The close on the last line is checked because it can still fail (a deferred write error surfaces
// there); the ones on the way out are not, since the caller already has a reason to discard the file.
func fill(tmp *os.File, data []byte, mode os.FileMode) error {
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	return tmp.Close()
}

// syncDir flushes the directory itself, which is where the new name lives.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}
