package loop

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/produce"
)

// always is a probe that says yes: the tests below are about the executor's arithmetic, not about
// which condition fired.
type always struct{ name string }

func (p always) Name() string         { return p.name }
func (p always) Every() time.Duration { return time.Second }
func (p always) Cost() produce.Cost   { return produce.Cheap }
func (p always) Read(env produce.Env) produce.Reading {
	return produce.Reading{State: produce.Fresh, Value: true, At: env.Now}
}

// writeGlue writes one of the job's own numbers.
func writeGlue(t *testing.T, dir, name, value string) {
	t.Helper()
	base := filepath.Join(dir, advise.BaseDir)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, name), []byte(value+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestTheEstimateIsASeparateChannel is the estimate channel's acceptance: before one write has been measured, the executor
// has an estimate, and it says where the number came from. A measured p90 always wins, and an estimate
// never enters the measured samples.
func TestTheEstimateIsASeparateChannel(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l, err := Open(Config{
		Dir: dir, UnitID: "u",
		Bounds:     policy.Bounds{NoUs: policy.NoUsNone, MinCopies: 1},
		Policy:     policy.Risk{},
		Now:        func() time.Time { return now },
		Throughput: 41 << 20, // 41 MiB/s, a number a test can state
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1.2 GiB, declared by the job, over a probed throughput: an estimate computed from a declaration.
	l.glue = Glue{ExpectedBytes: 1288490188}
	seconds, source := l.estimate()
	fmt.Printf("estimate 1.2GiB @ 41MB/s = %.0fs (%s)\n", seconds, source)
	if source != "probe" || seconds < 29 || seconds > 31 {
		t.Fatalf("estimate = %.1fs (%s), want about 30s from the probe", seconds, source)
	}

	// What the job says its own last save took beats a computed estimate.
	l.glue = Glue{LastSaveSeconds: 250}
	if s, src := l.estimate(); src != "job" || s != 250 {
		t.Fatalf("estimate = %.1fs (%s), want the job's own 250s", s, src)
	}

	// A measured p90 beats both, and the samples stay measured-only.
	l.ledger.WriteCosts = []float64{250}
	measured, source := l.estimate()
	fmt.Printf("measured p90 %s (%s)\n", time.Duration(measured*float64(time.Second)), source)
	if source != "measured" || measured != 250 {
		t.Fatalf("estimate = %.1fs (%s), want the measured 250s", measured, source)
	}
	if len(l.ledger.WriteCosts) != 1 {
		t.Fatalf("measured samples = %d, want the single sealed write", len(l.ledger.WriteCosts))
	}
}

// TestAWriteThatCannotFinishIsRefusedOutLoud is the no-time refusal's acceptance: a deadline is announced, the write does
// not fit in it, and the refusal states both halves — what the write needs and what it would cost —
// instead of starting a checkpoint that would die with the job.
func TestAWriteThatCannotFinishIsRefusedOutLoud(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// There is one checkpoint, sealed just now, and the job has moved past it: what is at risk is the
	// step it is on, not the step already on disk.
	writeSealed(t, dir, 10, 100)
	writeGlue(t, dir, "step", "11")
	writeGlue(t, dir, GlueLastSave, "240")   // a write takes four minutes
	writeGlue(t, dir, GlueSecondsLeft, "30") // the platform gives us thirty seconds

	l, err := Open(Config{
		Dir: dir, UnitID: "u",
		Bounds: policy.Bounds{NoUs: policy.NoUsNone, MinCopies: 1},
		Policy: policy.Risk{}, Now: func() time.Time { return now },
		Probes: []produce.Probe{always{"risk.preempt"}}, VerifyEvery: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := l.Round()
	if err != nil {
		t.Fatal(err)
	}
	if st.Gate != "no-time" || st.Attempts.Refused != 1 {
		t.Fatalf("gate = %q, attempts = %s, want a no-time refusal recorded", st.Gate, st.Attempts)
	}

	// Twelve minutes later the window is still thirty seconds and the write still four minutes: the
	// refusal now also says what it costs, which is the only thing that makes it a report.
	now = now.Add(12 * time.Minute)
	st, err = l.Round()
	if err != nil {
		t.Fatal(err)
	}
	var note string
	for _, n := range st.Notes {
		if strings.Contains(n, "work at risk") {
			note = n
		}
	}
	fmt.Printf("%s\n", note)
	if !strings.Contains(note, "12m of work at risk") || !strings.Contains(note, "no-time") {
		t.Fatalf("the refusal does not state the loss: %v", st.Notes)
	}
	if st.Action != policy.ActionSkip {
		t.Fatalf("action = %q, want a skip: nothing can start", st.Action)
	}
}

// A price is only usable if the sentence says where it came from. "4.2s" with no provenance is the
// number this product exists to replace; "4.2s (2.0 GB at 1.7 GB/s)" is a claim a person can check.
func TestThePriceSaysWhereItCameFrom(t *testing.T) {
	cases := []struct {
		name  string
		price WritePrice
		want  string
	}{
		{"unknown", WritePrice{}, "unknown"},
		{"probe", WritePrice{Seconds: 1.2, Source: "probe", Bytes: 2147483648, Throughput: 1825361100}, "1.2s (2.0 GB at 1.7 GB/s)"},
		{"job", WritePrice{Seconds: 4.5, Source: "job"}, "4.5s (the job declared it)"},
		{"measured", WritePrice{Seconds: 9, Source: "measured"}, "9.0s (measured)"},
	}
	for _, c := range cases {
		if got := c.price.Describe(); !strings.Contains(got, c.want) {
			t.Errorf("%s: Describe() = %q, want it to contain %q", c.name, got, c.want)
		}
	}
}

// The start-up price must be readable before any round has run, or the first thing a user sees when
// deciding whether to configure anything is "we will tell you later".
func TestThePriceIsAvailableBeforeTheFirstRound(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ckpt-governor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ckpt-governor", "expected-bytes"), []byte("2097152"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := Open(Config{Dir: dir, UnitID: "u", Bounds: policy.Bounds{}, Throughput: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	p := l.PriceAtStart()
	if p.Seconds <= 0 || p.Source == "unknown" {
		t.Fatalf("no price before the first round: %+v", p)
	}
	if got := p.Describe(); !strings.Contains(got, "the job") && !strings.Contains(got, "at ") {
		t.Fatalf("the price does not explain itself: %q", got)
	}
}

// TestPriceOfDoesNotProbeWithoutADeclaredSize keeps `price` read-only: on a directory where the job
// declared no size there is nothing to price, so the 32 MiB throughput probe must not run.
func TestPriceOfDoesNotProbeWithoutADeclaredSize(t *testing.T) {
	dir := t.TempDir()
	before := namesUnder(t, dir)
	p := PriceOf(dir)
	after := namesUnder(t, dir)
	if after != before {
		t.Fatalf("PriceOf changed the directory: %q -> %q", before, after)
	}
	if p.Source != "unknown" || p.Seconds != 0 {
		t.Fatalf("price on a cold directory = %+v, want unknown", p)
	}
	fmt.Printf("price: cold directory untouched, price unknown\n")
}

// TestPriceOfProbesWhenTheSizeIsDeclared keeps the one case that legitimately probes: a declared size
// with no history is exactly what the throughput probe is for.
func TestPriceOfProbesWhenTheSizeIsDeclared(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, advise.BaseDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, advise.BaseDir, "expected-bytes"), []byte("2097152"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := PriceOf(dir)
	if p.Source != "probe" || p.Seconds <= 0 {
		t.Fatalf("declared size did not price: %+v", p)
	}
	fmt.Printf("price: declared 2 MiB priced by probe (%s)\n", p.Describe())
}

// namesUnder lists every path under dir, sorted, so a test can say "nothing appeared".
func namesUnder(t *testing.T, dir string) string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != dir {
			names = append(names, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}
