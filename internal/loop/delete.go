package loop

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// trashDir holds retired copies until they are swept. It lives inside the checkpoint directory so
// that the rename is on one volume and therefore atomic; the scanner ignores it because its name is
// not `step-<N>`.
const trashDir = ".trash"

// Deleted is one removed copy, for the log and the status view.
type Deleted struct {
	Step  int64
	Bytes int64
}

// retireBelow retires the copies retirableBelow selects, by renaming each into the trash.
//
// Removal is a rename into `.trash`, not a delete: a rename is microseconds and atomic, while
// `RemoveAll` on a large directory blocks the whole round on metadata work. The space is reclaimed by
// sweeping, in small bites, on later rounds.
func retireBelow(dir string, oldest int64, keep int, copies []policy.Copy, now time.Time) ([]Deleted, error) {
	var out []Deleted
	for _, c := range retirableBelow(oldest, keep, copies) {
		// The directory on disk is named by whoever wrote it: `step-N` for the older writer, `gen-N` for the
		// contract writer (0025). Retiring a name we assumed produced `rename ...: no such file or directory`
		// inside a repair path and the run carried on — a quiet failure in the one place that must be loud
		// (F11).
		name := copyDirName(dir, c.Step)
		if name == "" {
			return out, fmt.Errorf("nothing to retire for step %d: neither step-%d nor gen-%d exists",
				c.Step, c.Step, c.Step)
		}
		src := filepath.Join(dir, name)
		dst := filepath.Join(dir, trashDir, name+"-"+strconv.FormatInt(now.UnixNano(), 10))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return out, err
		}
		if err := os.Rename(src, dst); err != nil {
			return out, err
		}
		out = append(out, Deleted{Step: c.Step, Bytes: c.Bytes})
	}
	return out, nil
}

// retirableBelow selects the copies that retention no longer protects.
//
// The five rules are enforced here rather than trusted to the caller:
//
//  1. only sealed or broken copies are eligible — an unverified directory may be a write in flight;
//  2. the newest `keep` **copies** are never removed, whatever the step says. A partial directory is
//     not a copy, so it must not consume one of those places: counting it would retire a good
//     checkpoint to make room for a write that may never finish;
//  3. a copy is removed only when its step is below oldest;
//  4. the last sealed copy is never removed: a broken write at a higher step counts toward `keep`,
//     and without this rule it takes the only good checkpoint with it;
//  5. the newest copy is never removed, whatever its state. A broken newest copy is the evidence the
//     policy needs to ask for a repair: retire it, and the reason to repair disappears with it.
//
// It touches nothing on disk: that is what lets `--dry-run` print the same choice the real round
// would act on, without a second set of rules that could drift from this one.
func retirableBelow(oldest int64, keep int, copies []policy.Copy) []policy.Copy {
	// A decision may say "keep the newest N" without naming a step (risk-v1 keeps the count and
	// drops the step threshold). Zero means no step rule, not "keep everything".
	ordered := append([]policy.Copy(nil), copies...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Step > ordered[j].Step })
	if len(ordered) == 0 {
		return nil
	}

	// The directories that could be restored from. A partial directory is a write in flight, so it
	// never counts as a copy and never consumes one of the places `keep` protects.
	var existing []policy.Copy
	for _, c := range ordered {
		if c.State == policy.CopySealed || c.State == policy.CopyVerified || c.State == policy.CopyBroken {
			existing = append(existing, c)
		}
	}
	sealedLeft := 0
	for _, c := range existing {
		if c.State == policy.CopySealed || c.State == policy.CopyVerified {
			sealedLeft++
		}
	}

	newest := ordered[0].Step
	var out []policy.Copy
	for i, c := range existing {
		if c.Step >= newest {
			continue // the newest thing on disk is evidence, not clutter
		}
		if i < keep {
			continue // the newest `keep` copies are protected by the count
		}
		if oldest > 0 && c.Step >= oldest {
			continue
		}
		if c.State == policy.CopySealed || c.State == policy.CopyVerified {
			if sealedLeft <= 1 {
				continue
			}
			sealedLeft--
		}
		out = append(out, c)
	}
	return out
}

// sweepTrash removes retired copies, spending at most budget of wall time.
//
// Bounded on purpose: a 1 TB directory takes minutes to unlink, and a round that takes minutes is a
// round that misses the trigger. Whatever is left is picked up next round.
func sweepTrash(dir string, budget time.Duration) (int, error) {
	trash := filepath.Join(dir, trashDir)
	entries, err := os.ReadDir(trash)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	deadline := time.Now().Add(budget)
	removed := 0
	for _, e := range entries {
		if !time.Now().Before(deadline) {
			break
		}
		if err := os.RemoveAll(filepath.Join(trash, e.Name())); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// copyDirName is the directory on disk for a step, whichever writer made it.
//
// Returns "" when neither layout is present: callers must refuse rather than rename a name they imagined.
func copyDirName(dir string, step int64) string {
	for _, name := range []string{
		"step-" + strconv.FormatInt(step, 10),
		"gen-" + strconv.FormatInt(step, 10),
		fmt.Sprintf("gen-%04d", step),
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return name
		}
	}
	return ""
}
