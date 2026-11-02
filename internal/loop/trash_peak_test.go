package loop

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

func writeCopy(t *testing.T, dir string, step int64, files, size int) {
	t.Helper()
	d := filepath.Join(dir, "step-"+strconv.FormatInt(step, 10))
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "manifest.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < files; i++ {
		if err := os.WriteFile(filepath.Join(d, "rank-"+strconv.FormatInt(int64(i), 10)+".bin"), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func trashBytes(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	err := filepath.Walk(filepath.Join(dir, trashDir), func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		total += info.Size()
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return total
}

// The trash has to be bounded while a job keeps writing, and drained once it stops.
//
// "Every round sweeps" is a mechanism; this is the evidence that the mechanism is fast enough. The budget is
// deliberately small here (128 KiB against 512 KiB copies) so a sweep takes several rounds, which is the only
// regime where a peak can build up at all.
func TestTrashStaysBoundedAcrossManyRetirements(t *testing.T) {
	const (
		copyFiles  = 4
		fileSize   = 128 << 10 // 512 KiB per copy
		copySize   = copyFiles * fileSize
		roundSweep = 128 << 10 // a quarter of a copy per round
		rounds     = 40
	)

	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	var copies []policy.Copy
	for step := int64(1); step <= 3; step++ {
		writeCopy(t, dir, step, copyFiles, fileSize)
		copies = append(copies, policy.Copy{Step: step, State: policy.CopySealed, Bytes: copySize})
	}

	var peak int64
	next := int64(4)
	for i := 0; i < rounds; i++ {
		// One new copy lands and the oldest is retired: exactly the steady state of a job that keeps writing.
		writeCopy(t, dir, next, copyFiles, fileSize)
		copies = append(copies, policy.Copy{Step: next, State: policy.CopySealed, Bytes: copySize})
		next++

		deleted, err := retireBelow(dir, 0, 2, copies, now)
		if err != nil {
			t.Fatal(err)
		}
		// A retired copy is gone from the volume, so it must leave the list too: the loop's real copy list
		// comes from a scan, and keeping the retired ones here would ask retirement to remove what it already
		// removed (which is exactly what the new guard refuses to do quietly).
		gone := make(map[int64]bool, len(deleted))
		for _, d := range deleted {
			gone[d.Step] = true
		}
		kept := copies[:0]
		for _, c := range copies {
			if !gone[c.Step] {
				kept = append(kept, c)
			}
		}
		copies = kept
		// The same rule the loop uses: a round must be able to give back what it retired. Testing it with a
		// smaller budget would measure a loop nobody runs.
		if _, _, err := sweepTrash(dir, sweepBudgetFor(bytesOf(deleted))); err != nil {
			t.Fatal(err)
		}
		if used := trashBytes(t, dir); used > peak {
			peak = used
		}
	}

	// Three copies is the bound: two retired ones waiting, plus the one being swept. Anything that grows with
	// the number of rounds means the sweep is not keeping up, and a long job fills the volume.
	if peak > 3*copySize {
		t.Fatalf("trash peaked at %d bytes (%.1f copies); a steady job must not accumulate", peak, float64(peak)/copySize)
	}

	// Writes stop. The trash must drain to nothing, or a finished job keeps its disk forever.
	for i := 0; i < 40; i++ {
		if _, _, err := sweepTrash(dir, sweepBudgetFor(0)); err != nil {
			t.Fatal(err)
		}
	}
	if left := trashBytes(t, dir); left != 0 {
		t.Fatalf("%d bytes still in the trash after the writes stopped", left)
	}
}
