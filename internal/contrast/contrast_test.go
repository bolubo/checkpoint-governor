package contrast

import (
	"strings"
	"testing"
	"time"
)

// The table's contract: every row names what it covers, and the counts are the ones the policies
// actually produce — the bounded arm asks the real loop, so this test also covers the window trigger.
func TestEveryRowNamesItsCoverageAndItsWrites(t *testing.T) {
	o := Options{
		Steps:          2000,
		Step:           time.Second,
		WriteCost:      30 * time.Second,
		SaveEverySteps: 500,
		Bound:          30 * time.Minute,
	}
	rows, err := Run(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4: %+v", len(rows), rows)
	}

	want := map[string]int{
		"500 steps (framework default)": 4, // 2000/500
		"nothing installed":             0,
		"governor, unconfigured":        1, // the job's final write
		"30m bound":                     2, // the window at 30m, then the final write
	}
	for _, r := range rows {
		if r.NoSignal == "" {
			t.Errorf("%q prints writes without a worst case: %+v", r.Policy, r)
		}
		w, ok := want[r.Policy]
		if !ok {
			t.Fatalf("unexpected policy row %q", r.Policy)
		}
		if r.Writes != w {
			t.Errorf("%q writes = %d, want %d", r.Policy, r.Writes, w)
		}
	}

	// The unconfigured default is the whole point: one write, and a death that announces nothing still
	// costs everything. The table says so, so nobody can read it as a guarantee. This model has no
	// conditions, so an announced stop is not a column here; `risk.shutdown` is the executor's, and the
	// real-machine work measures it.
	for _, r := range rows {
		if r.Policy == "governor, unconfigured" {
			if r.NoSignal != "everything" {
				t.Errorf("unconfigured row claims a worst case of %q; it has everything", r.NoSignal)
			}
		}
		if strings.HasPrefix(r.Policy, "30m bound") {
			if r.NoSignal != "≤30m" {
				t.Errorf("bounded row worst case = %q, want ≤30m", r.NoSignal)
			}
			if r.LostSteps != 1800 {
				t.Errorf("bounded row lost steps = %d, want 1800", r.LostSteps)
			}
		}
	}
}

func TestShort(t *testing.T) {
	cases := map[time.Duration]string{
		0:                "0s",
		40 * time.Second: "40s",
		2 * time.Hour:    "2h",
		30 * time.Minute: "30m",
	}
	for d, want := range cases {
		if got := Short(d); got != want {
			t.Errorf("Short(%s) = %q, want %q", d, got, want)
		}
	}
}
