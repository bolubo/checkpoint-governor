package resume

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/verify"
)

// Sealed is one generation that exists: the directory, and the manifest that makes it exist.
//
// A generation directory without a manifest is a write in flight, not a copy (0025): it is never listed
// here, because every question below is asked about something that finished.
type Sealed struct {
	Dir      string
	Manifest verify.Manifest
}

// GenerationPrefix names the writer's directories. The number is the generation, and the leading zeros
// are formatting only.
const GenerationPrefix = "gen-"

// Generations lists the sealed generations under root, oldest first. An unreadable manifest is skipped
// for the same reason: a file we cannot believe does not make a copy exist, and the caller's newest
// question must not be answered with a guess.
func Generations(root string) ([]Sealed, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Sealed
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), GenerationPrefix) {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimPrefix(e.Name(), GenerationPrefix), 10, 64)
		if err != nil || n <= 0 {
			continue
		}
		dir := filepath.Join(root, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			continue
		}
		m, err := verify.Parse(raw)
		if err != nil {
			continue
		}
		out = append(out, Sealed{Dir: dir, Manifest: m})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Manifest.Generation < out[j].Manifest.Generation })
	return out, nil
}

// NewestSealed is the newest generation that exists.
func NewestSealed(root string) (Sealed, bool, error) {
	gens, err := Generations(root)
	if err != nil || len(gens) == 0 {
		return Sealed{}, false, err
	}
	return gens[len(gens)-1], true, nil
}

// Generation returns one sealed generation by number.
func Generation(root string, want int64) (Sealed, bool, error) {
	gens, err := Generations(root)
	if err != nil {
		return Sealed{}, false, err
	}
	for _, g := range gens {
		if g.Manifest.Generation == want {
			return g, true, nil
		}
	}
	return Sealed{}, false, nil
}

// SealedBefore is the newest generation sealed at or before t. It answers the only question an adoption
// verdict can honestly ask: which checkpoint existed when this job started. `sealedAt` orders
// generations and is never used to decide whether something is late (0025). A zero t means no bound.
func SealedBefore(root string, t time.Time) (Sealed, bool, error) {
	gens, err := Generations(root)
	if err != nil {
		return Sealed{}, false, err
	}
	if t.IsZero() {
		if len(gens) == 0 {
			return Sealed{}, false, nil
		}
		return gens[len(gens)-1], true, nil
	}
	var best Sealed
	found := false
	for _, g := range gens {
		if g.Manifest.SealedAt.After(t) {
			continue
		}
		if !found || g.Manifest.Generation > best.Manifest.Generation {
			best, found = g, true
		}
	}
	return best, found, nil
}
