package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/testutil"
)

// setTreeTimes stamps every entry of a step directory. A copy's time is the newest modification
// inside it (observe reads "is this write still moving"), not the directory's own mtime.
func setTreeTimes(t *testing.T, root string, at time.Time) {
	t.Helper()
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, at, at)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A directory with no history has no price. The sheet must say so rather than pick a ladder value
// and call it a recommendation: an unmeasured write cost is the one number this command may not
// invent (product-v1 §16).
func TestPriceWithNoHistoryPromisesNothing(t *testing.T) {
	dir := t.TempDir()
	testutil.Sealed(t, dir, 100, 1_000_000)
	testutil.Sealed(t, dir, 200, 1_000_000)

	var buf bytes.Buffer
	if code := runPrice([]string{dir}, &buf); code != 0 {
		t.Fatalf("price on a cold directory exited %d", code)
	}
	out := buf.String()
	for _, want := range []string{
		"write cost      unknown (no write has been measured",
		"your habit      none yet",
		"nothing  exceptions only",
		"tax budget 1% ⇒ unreachable",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "% of the run") {
		t.Fatalf("a price was printed without a measurement:\n%s", out)
	}
	fmt.Printf("price: cold directory → unknown write cost, no price, no promise\n")
}

// The positive path: five seals ten minutes apart and a measured 30s write give the habit, the
// ladder, and the budget floor. This is the sentence a platform team can take to a meeting.
func TestPricePrintsTheHabitAndTheBudget(t *testing.T) {
	dir := t.TempDir()
	origin := time.Now().Add(-59 * time.Minute)
	for i := int64(1); i <= 5; i++ {
		step := i * 100
		testutil.Sealed(t, dir, step, 1_000_000)
		at := origin.Add(time.Duration(i) * 10 * time.Minute)
		setTreeTimes(t, filepath.Join(dir, testutil.StepDir(step)), at)
	}
	led, err := advise.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	led.WriteCosts = []float64{28, 29, 30, 30, 30}
	if err := led.Save(dir); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if code := runPrice([]string{"--for=4h", dir}, &buf); code != 0 {
		t.Fatalf("price exited %d", code)
	}
	out := buf.String()
	for _, want := range []string{
		"write cost      30s (measured: p90 of 5 sealed writes)",
		"your habit      period 10m · worst staleness 10m (5 sealed copies)",
		"10m ",
		"24 writes",
		"5.0% of the run",
		"tax budget 1% ⇒ the tightest bound this mount can hold is 50m",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	// The count is the run's, not the habit's: 4h at a 10m bound is 24 windows.
	if !strings.Contains(out, "\n10m       24 writes") {
		t.Fatalf("the sheet did not price the 10m bound against the 4h run:\n%s", out)
	}
	fmt.Printf("price: 5 seals · 10m habit · 30s write → 10m bound costs 24 writes and 5.0%% of a 4h run\n")
}

// Without --for the sheet prices the tax and refuses to count: a count needs a run, and guessing
// the run would be the same invention as guessing the write cost.
func TestPriceWithoutARunCountsNothing(t *testing.T) {
	dir := t.TempDir()
	led, err := advise.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	led.WriteCosts = []float64{30, 30, 30}
	if err := led.Save(dir); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if code := runPrice([]string{"--tax=0.05", dir}, &buf); code != 0 {
		t.Fatalf("price exited %d", code)
	}
	out := buf.String()
	// "3 sealed writes" is the provenance line; a count of window writes looks like "24 writes" and
	// must not appear without a run length.
	if regexp.MustCompile(`\d+ writes`).MatchString(out) {
		t.Fatalf("write counts were printed without a run length:\n%s", out)
	}
	if !strings.Contains(out, "tax budget 5% ⇒ the tightest bound this mount can hold is 10m") {
		t.Fatalf("the budget floor is missing:\n%s", out)
	}
	fmt.Printf("price: no --for → tax only, and 5%% of the run still needs a 10m bound\n")
}
