package loop

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// trashDir holds retired copies until they are swept. It lives inside the checkpoint directory so
// that the rename is on one volume and therefore atomic; the scanner ignores it because its name is
// not `step-<N>`.
const trashDir = ".trash"

// sweepBytesPerRound is how much of the trash one round may give back.
//
// 256 MiB keeps a round short on any volume while still clearing a retired checkpoint in a few rounds: the
// alternative, a wall-clock budget, deleted less than it promised on a slow disk and more on a fast one, and
// could not be interrupted in the middle of a directory at all.
const sweepBytesPerRound = 256 << 20

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

// sweepTrash removes retired copies until budgetBytes of file content is gone.
//
// Bounded in **bytes**, not wall time: time is the wrong measure for deletion. A slow volume deletes less
// than the budget and a fast one more, and `os.RemoveAll` on a large directory cannot be interrupted at all —
// so a time budget both overruns and under-delivers. Deleting file by file also makes the budget precise and
// the round short, which is what a controller needs.
//
// A partially deleted directory is left in the trash and finished next round: the bytes in it are already
// accounted for, so a half-swept directory cannot be mistaken for a whole one.
//
// budgetBytes <= 0 removes nothing. That is the dry-run value, and it is the safe default.
func sweepTrash(dir string, budgetBytes int64) (removed int, freed int64, err error) {
	trash := filepath.Join(dir, trashDir)
	entries, err := os.ReadDir(trash)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}

	for _, e := range entries {
		if freed >= budgetBytes {
			break
		}
		path := filepath.Join(trash, e.Name())
		n, gone, err := removeWithin(path, budgetBytes-freed)
		freed += n
		if err != nil {
			return removed, freed, err
		}
		if gone {
			removed++
		}
	}
	return removed, freed, nil
}

// removeWithin deletes files under path until it has freed `limit` bytes, then returns. `gone` says whether
// the directory itself is now empty and was removed.
func removeWithin(path string, limit int64) (freed int64, gone bool, err error) {
	err = filepath.Walk(path, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || freed >= limit {
			return nil
		}
		size := info.Size()
		if rmErr := os.Remove(p); rmErr != nil {
			return rmErr
		}
		freed += size
		return nil
	})
	if err != nil {
		return freed, false, err
	}

	// The directory itself goes only when nothing is left inside it: an empty tree that failed halfway must
	// keep its name, or the next round cannot find it to finish.
	if rmErr := os.Remove(path); rmErr == nil {
		return freed, true, nil
	}
	return freed, false, nil
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

// freeBytes is the space available to an unprivileged writer on the volume holding dir.
func freeBytes(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// largestCopy is the size of the biggest copy on disk, which is the size the next write will need.
func largestCopy(copies []policy.Copy) int64 {
	var max int64
	for _, c := range copies {
		if c.Bytes > max {
			max = c.Bytes
		}
	}
	return max
}

// keepUnderPressure lowers how many copies retention keeps when the volume is nearly full.
//
// Writing into a full disk turns one bad checkpoint into a busy volume, and a rewrite that cannot land is
// worse than a smaller retention: the newest copy is the one anybody comes back for, so under pressure the
// answer is keep one and keep writing, not keep three and fail.
func keepUnderPressure(dir string, wanted int, copies []policy.Copy) int {
	size := largestCopy(copies)
	if size == 0 || wanted <= 1 {
		return wanted
	}
	free, err := freeBytes(dir)
	if err != nil {
		return wanted
	}
	// Two copies of headroom: the one being written and one to land on.
	if free < 2*size {
		return 1
	}
	return wanted
}
