// Package drill runs a loader against a sealed checkpoint and decides whether it took a step.
//
// It is the only thing in the product that earns the word `restorable` (decision 0026). A declaration can
// say anything and a receipt cannot be argued with, so the proof is a real process, one step, and the
// files before and after — a drill that changed the checkpoint is refused, not passed.
package drill

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// TailBytes is how much of a file's end is hashed. The same bound the verifier uses: a kill leaves its
// damage at the end of a file, and the whole point of the fingerprint is to notice a change, not to prove
// the absence of one.
const TailBytes = 1 << 20

// Snapshot is what the tree looked like. Paths are relative to the root so two runs are comparable even
// if the directory moved.
type Snapshot struct {
	root    string
	entries map[string]string
}

// Take walks the tree and records, for every file, its size, modification time and tail hash.
//
// The tail hash is what catches "the loader wrote into a shard": a size-and-mtime comparison already
// would, but mtime can be preserved by a copy and a size can be preserved by an overwrite.
func Take(root string) (Snapshot, error) {
	s := Snapshot{root: root, entries: map[string]string{}}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			// A drill that replaces a shard with a link to elsewhere is a change, and following it would
			// read data that is not in the checkpoint.
			s.entries[rel(root, path)] = fmt.Sprintf("symlink|%d", info.Size())
			return nil
		}
		tail, err := tailHash(path, info.Size())
		if err != nil {
			return err
		}
		s.entries[rel(root, path)] = fmt.Sprintf("%d|%d|%s", info.Size(), info.ModTime().UnixNano(), tail)
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

// Difference returns the first path that differs between two snapshots, and why.
func (s Snapshot) Difference(other Snapshot) (string, bool) {
	names := map[string]bool{}
	for name := range s.entries {
		names[name] = true
	}
	for name := range other.entries {
		names[name] = true
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		a, aok := s.entries[name]
		b, bok := other.entries[name]
		switch {
		case !aok:
			return name + " appeared", true
		case !bok:
			return name + " disappeared", true
		case a != b:
			return name + " changed", true
		}
	}
	return "", false
}

func tailHash(path string, size int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	start := size - TailBytes
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func rel(root, path string) string {
	r, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return r
}
