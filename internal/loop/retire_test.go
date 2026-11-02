package loop

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/testutil"
)

// TestRetirementKeepsTheCopiesThatMatter is the retirement rule's acceptance.
//
// Three rules are visible here: a partial directory is a write and not a copy, so it does not consume
// one of the places retention keeps; the last sealed copy is never taken, whatever the count says;
// and the sweep stops inside its budget, because a round that unlinks for minutes is a round that
// misses the next trigger.
func TestRetirementKeepsTheCopiesThatMatter(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// Five sealed copies and one write in flight at a higher step.
	for step := int64(1); step <= 5; step++ {
		testutil.Sealed(t, dir, step, 100)
	}
	testutil.Unsealed(t, dir, 6, 10)
	copies := []policy.Copy{
		{Step: 1, State: policy.CopySealed}, {Step: 2, State: policy.CopySealed},
		{Step: 3, State: policy.CopySealed}, {Step: 4, State: policy.CopySealed},
		{Step: 5, State: policy.CopySealed}, {Step: 6, State: policy.CopyUnverified},
	}
	deleted, err := retireBelow(dir, 0, 2, copies, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 3 {
		t.Fatalf("deleted %d copies, want 3", len(deleted))
	}
	for _, d := range deleted {
		if d.Step == 5 || d.Step == 4 || d.Step == 6 {
			t.Fatalf("retired step %d, which must stay (newest two copies and the write in flight)", d.Step)
		}
	}
	for _, step := range []int64{4, 5, 6} {
		if _, err := os.Stat(filepath.Join(dir, dirName(step))); err != nil {
			t.Fatalf("step %d is gone: %v", step, err)
		}
	}
	fmt.Printf("retire: kept 2 deleted %d in-flight-skipped\n", len(deleted))

	// The last sealed copy is never taken, even when retention asks for zero and a broken copy sits
	// above it: the newest thing on disk may be unusable, and the only good one is the only one left.
	dir2 := t.TempDir()
	testutil.Sealed(t, dir2, 10, 100)
	testutil.Broken(t, dir2, 11)
	last, err := retireBelow(dir2, 0, 0, []policy.Copy{
		{Step: 10, State: policy.CopySealed}, {Step: 11, State: policy.CopyBroken},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(last) != 0 {
		t.Fatalf("retired %v with the last good copy at stake", last)
	}
	fmt.Printf("retire: last-copy-protected (kept step 10, broke step 11 stays)\n")

	// The sweep is bounded in wall time, not in entries: a budget already spent removes nothing, and
	// what is left is picked up next round. (A fast temporary file system makes a small positive
	// budget remove everything, so the deterministic case is zero.)
	pending, err := os.ReadDir(filepath.Join(dir, trashDir))
	if err != nil {
		t.Fatal(err)
	}
	removed, _, err := sweepTrash(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("swept %d with no budget", removed)
	}
	removed, _, err = sweepTrash(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("sweep: %d pending, 0 removed with no budget, %d removed with a generous one\n", len(pending), removed)
	if removed != len(pending) {
		t.Fatalf("swept %d of %d with a generous budget", removed, len(pending))
	}
}
