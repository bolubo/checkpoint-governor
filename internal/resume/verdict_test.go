package resume_test

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bolubo/checkpoint-governor/internal/resume"
)

// One row per line of the contract's table. The fixture and the world are the inputs; the sentence and
// the exit code are the product.
func TestVerdictMatrix(t *testing.T) {
	cases := []struct {
		fixture string
		asked   resume.World
		want    string
		code    int
	}{
		{"full", resume.World{}, "resumable (gen 512, step 512, world 8/8, full state)", 0},
		{"weights-only", resume.World{}, "not resumable: optimizer state absent", 1},
		{"partial-optimizer", resume.World{}, "not resumable: declared partial (optimizer) — this is a fine-tune, not a resume", 1},
		{"reshardable", resume.World{Ranks: 16}, "resumable with resharding (8 → 16 ranks)", 0},
		{"full", resume.World{Ranks: 16}, "not resumable: world changed 8 → 16, and this format is not reshardable", 1},
		{"unknown-format", resume.World{}, "unknown: resume.json: format \"nemo\" is not one we can judge", 2},
		{"no-resume", resume.World{}, "unknown: sealed, but nobody said what it holds", 2},
		{"invalid", resume.World{}, "unknown: resume.json:", 2},
	}

	for _, c := range cases {
		t.Run(c.fixture+"@"+strconv.Itoa(c.asked.Ranks), func(t *testing.T) {
			gen := singleGeneration(t, filepath.Join("testdata", c.fixture))
			f, declared, err := resume.Load(gen)
			v := resume.Judge(f, declared, err, c.asked)
			if !strings.HasPrefix(v.Line(), c.want) {
				t.Fatalf("verdict is %q, want it to start with %q", v.Line(), c.want)
			}
			if got := v.ExitCode(); got != c.code {
				t.Fatalf("exit %d, want %d (%s)", got, c.code, v.Line())
			}
		})
	}
}

// A verdict names what is missing, not just that something is: the person reading it has to know which
// state to ask their library to save.
func TestAVerdictNamesEveryMissingState(t *testing.T) {
	gen := singleGeneration(t, filepath.Join("testdata", "weights-only"))
	f, declared, err := resume.Load(gen)
	if err != nil || !declared {
		t.Fatalf("fixture: declared=%v err=%v", declared, err)
	}
	v := resume.Judge(f, declared, err, resume.World{})
	want := []string{"optimizer", "scheduler", "rng", "dataloader"}
	if len(v.Missing) != len(want) {
		t.Fatalf("missing %v, want %v", v.Missing, want)
	}
	for i := range want {
		if v.Missing[i] != want[i] {
			t.Fatalf("missing %v, want %v", v.Missing, want)
		}
	}
	if !strings.Contains(v.Line(), "(also: scheduler, rng, dataloader)") {
		t.Fatalf("the line loses the other missing states: %q", v.Line())
	}
}

// A world field the declaration never stated is not a promise that it matches the restore. Asking for 16
// ranks against a file that does not say how many it had is a refusal, not a guess.
func TestAnUndeclaredTopologyIsNotAnAssumptionOfEquality(t *testing.T) {
	f := resume.File{
		Schema: resume.Schema, Generation: 1, Step: 1, Format: resume.FormatDCP,
		World: resume.World{Ranks: 8},
		State: resume.State{Model: true, Optimizer: true, Scheduler: true, RNG: true, Dataloader: true},
	}
	v := resume.Judge(f, true, nil, resume.World{Ranks: 8, TP: 2})
	if v.Reason != resume.ReasonWorldChanged {
		t.Fatalf("an asked tp=2 against an undeclared tp was %q, want a refusal", v.Reason)
	}
}
