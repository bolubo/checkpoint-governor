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

// A declaration that names its own requirements is judged against them, and the sentence says which
// ruler was used. This is the Hugging Face case: four states, because a Trainer never saves a data
// position.
func TestAFrameworkDeclarationIsJudgedAgainstTheRulerItNames(t *testing.T) {
	f := resume.File{
		Schema: resume.Schema, Generation: 1000, Step: 1000, Format: resume.FormatHFTrainer,
		World:    resume.World{Ranks: 1},
		State:    resume.State{Model: true, Optimizer: true, Scheduler: true, RNG: true},
		Requires: []string{"model", "optimizer", "scheduler", "rng"},
	}
	v := resume.Judge(f, true, nil, resume.World{})
	if !v.Resumable() {
		t.Fatalf("four-of-four was %q: %s", v.Reason, v.Line())
	}
	want := "resumable (step 1000, requires model, optimizer, scheduler, rng)"
	if v.Line() != want {
		t.Fatalf("line = %q, want %q", v.Line(), want)
	}
	if strings.Contains(v.Line(), "dataloader") {
		t.Fatalf("a state the job never required was reported: %q", v.Line())
	}
}

// Only the required set can make a copy unresumable. A false state outside it is a fact the user may
// want to know, but it is not a refusal — and saying so would be judging the writer by someone else's
// ruler.
func TestAStateOutsideRequiresDoesNotBlockAResume(t *testing.T) {
	f := resume.File{
		Schema: resume.Schema, Generation: 1, Step: 1, Format: resume.FormatHFTrainer,
		World:    resume.World{Ranks: 1},
		State:    resume.State{Model: true, Optimizer: false, Scheduler: true, RNG: true},
		Requires: []string{"model", "scheduler", "rng"},
	}
	v := resume.Judge(f, true, nil, resume.World{})
	if !v.Resumable() {
		t.Fatalf("optimizer was not required, but the verdict was %q: %s", v.Reason, v.Line())
	}

	f.State.Optimizer, f.Requires = true, []string{"model", "optimizer"}
	if v := resume.Judge(f, true, nil, resume.World{}); !v.Resumable() {
		t.Fatalf("all required states were held, verdict %q", v.Reason)
	}
}

// A caveat does not change whether a restore works; it changes what the user is told it will do.
func TestACaveatIsPrintedOnTheSameLine(t *testing.T) {
	f := resume.File{
		Schema: resume.Schema, Generation: 1000, Step: 1000, Format: resume.FormatHFTrainer,
		World:    resume.World{Ranks: 1},
		State:    resume.State{Model: true, Optimizer: true, Scheduler: true, RNG: true},
		Requires: []string{"model", "optimizer", "scheduler", "rng"},
		Caveats:  []string{resume.CaveatDataPositionNotSaved},
	}
	v := resume.Judge(f, true, nil, resume.World{})
	if !v.Resumable() {
		t.Fatalf("a caveat made the copy unresumable: %s", v.Line())
	}
	if !strings.Contains(v.Line(), "caveat: the data position is not saved") {
		t.Fatalf("line = %q, want the caveat named", v.Line())
	}
	if len(v.Caveats) != 1 {
		t.Fatalf("JSON caveats = %v, want one", v.Caveats)
	}
}

// Every declaration written before `requires` existed still gets exactly the sentence it got then.
func TestADeclarationWithoutRequiresKeepsTodaysAnswer(t *testing.T) {
	gen := singleGeneration(t, filepath.Join("testdata", "weights-only"))
	f, declared, err := resume.Load(gen)
	if err != nil || !declared {
		t.Fatalf("fixture: declared=%v err=%v", declared, err)
	}
	if len(f.Requires) != 0 {
		t.Fatalf("fixture should predate requires, has %v", f.Requires)
	}
	v := resume.Judge(f, declared, err, resume.World{})
	if v.Line() != "not resumable: optimizer state absent (also: scheduler, rng, dataloader)" {
		t.Fatalf("line = %q, want the pre-requires answer", v.Line())
	}
	for _, name := range v.Requires {
		if name == "scaler" || name == "ema" {
			t.Fatalf("default requirements grew: %v", v.Requires)
		}
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
