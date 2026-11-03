package observe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Verification is the result of reading a checkpoint back.
type Verification struct {
	// Checked is false when the manifest carries no digest: a checkpoint that never promised a
	// digest cannot be called verified, and calling it broken would be worse.
	Checked bool
	// Detail is empty when the content matches, and names the first disagreement otherwise.
	Detail string
	// Bytes is how much was read, for the log: a verification that read nothing proves nothing.
	Bytes int64
}

// dirOfStep is the directory a step lives in. `step-<N>` answers by name; a generation directory
// carries its step in the manifest, so the generation number cannot be turned into a name. The lookup
// costs one ReadDir, which is nothing beside reading the bytes back.
func dirOfStep(root string, step int64) string {
	byName := dirName(step)
	if _, err := os.Stat(filepath.Join(root, byName, ManifestName)); err == nil {
		return byName
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return byName
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "gen-") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, e.Name(), ManifestName))
		if err != nil {
			continue
		}
		var m manifest
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		if m.Step == step {
			return e.Name()
		}
	}
	return byName
}

// Verify reads one checkpoint and compares every shard with the digest in the manifest.
//
// It is O(bytes) by nature — the only measurement in the product that is — so it is called from a
// sampled loop, never from a scan, and never while a write is in flight.
func Verify(dir string, step int64) (Verification, error) {
	name := dirOfStep(dir, step)
	raw, err := os.ReadFile(filepath.Join(dir, name, ManifestName))
	if err != nil {
		return Verification{}, err
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Verification{}, err
	}

	var v Verification
	for i, s := range m.Shards {
		if s.SHA256 == "" {
			continue
		}
		v.Checked = true
		path := filepath.Join(dir, name, s.fileName(i))
		sum, n, err := digest(path)
		if err != nil {
			return v, err
		}
		v.Bytes += n
		if sum != s.SHA256 {
			v.Detail = fmt.Sprintf("shard %d digest %s, the manifest says %s", s.Rank, short(sum), short(s.SHA256))
			return v, nil
		}
	}
	return v, nil
}

func digest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}
