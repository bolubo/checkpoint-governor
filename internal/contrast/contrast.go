// Package contrast answers one question with one table: what does the same job cost under four
// policies? It exists because the product's value is silence, and silence cannot be shown — only the
// counterfactual can.
//
// The governor arms are not modelled: they run the real loop with a virtual clock, and the writes they
// ask for are performed by this package (a sealed directory, stamped at the virtual time). The
// framework-default arm is a cadence, which is what a framework's `save_steps` is.
package contrast

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/loop"
	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// Options is the job one row of the table describes.
type Options struct {
	Steps          int           // 4000
	Step           time.Duration // 1s per step, so 4000 steps is four hours
	WriteCost      time.Duration // what one write costs on this disk
	SaveEverySteps int           // the framework default: save every N steps
	Bound          time.Duration // the promise a platform would make
}

// Default is a four-hour job on a disk where a write costs forty seconds. The numbers are a model of a
// job, not a measurement of a checkpoint, and the output says so.
func Default() Options {
	return Options{
		Steps:          14400,
		Step:           time.Second,
		WriteCost:      40 * time.Second,
		SaveEverySteps: 500,
		Bound:          2 * time.Hour,
	}
}

// Row is one policy and what it costs.
type Row struct {
	Policy string
	Writes int
	IO     time.Duration
	// NoSignal is what a death costs when no condition can fire: this model has no signals, so it has
	// no announcement to react to. An announced stop is `risk.shutdown` and belongs to the executor
	// (decision 0013), which is why it is not a column here: a table cannot claim a mechanism it does
	// not run.
	NoSignal  string
	LostSteps int // the worst case, in steps, for NoSignal
}

// Run builds the table. Every row names what it covers; a row with a count and no coverage would be a
// lie, so the caller can rely on both fields being set.
func Run(o Options) ([]Row, error) {
	if o.Steps <= 0 || o.Step <= 0 || o.WriteCost < 0 {
		return nil, fmt.Errorf("contrast: need positive steps and step duration (got %d steps, %s)", o.Steps, o.Step)
	}
	if o.SaveEverySteps <= 0 {
		return nil, fmt.Errorf("contrast: save-every-steps must be positive")
	}

	// Arm A: the framework default. A cadence writes whether anything is at risk, and it writes on any
	// step that is a multiple of the cadence — including the last one, when the job happens to end on it.
	cadence := Row{
		Policy:    fmt.Sprintf("%d steps (framework default)", o.SaveEverySteps),
		Writes:    o.Steps / o.SaveEverySteps,
		IO:        time.Duration(o.Steps/o.SaveEverySteps) * o.WriteCost,
		NoSignal:  fmt.Sprintf("≤%d steps", o.SaveEverySteps),
		LostSteps: o.SaveEverySteps,
	}

	// Arm B: nothing installed. The job never writes, so a death costs everything.
	nothing := Row{
		Policy:    "nothing installed",
		Writes:    0,
		IO:        0,
		NoSignal:  "everything",
		LostSteps: o.Steps,
	}

	// Arm C: the governor with nothing configured. It writes at the end. This model has no conditions,
	// so nothing can announce itself here; in the real loop a stop is `risk.shutdown` and is covered
	// one probe interval plus one round later (decision 0013). A death that announces nothing still
	// costs the whole job, and the row says so instead of hiding it.
	bare, err := governedWrites(o, 0)
	if err != nil {
		return nil, err
	}
	unconfigured := Row{
		Policy:    "governor, unconfigured",
		Writes:    bare + 1,
		IO:        time.Duration(bare+1) * o.WriteCost,
		NoSignal:  "everything",
		LostSteps: o.Steps,
	}

	// Arm C2: a promise a platform would make — at most the bound lost even on a death that announces
	// nothing.
	boundWrites, err := governedWrites(o, o.Bound)
	if err != nil {
		return nil, err
	}
	bounded := Row{
		Policy:    fmt.Sprintf("%s bound", Short(o.Bound)),
		Writes:    boundWrites + 1,
		IO:        time.Duration(boundWrites+1) * o.WriteCost,
		NoSignal:  "≤" + Short(o.Bound),
		LostSteps: int(o.Bound / o.Step),
	}

	return []Row{cadence, nothing, unconfigured, bounded}, nil
}

// governedWrites runs the real loop over the job and counts the writes it asks for. The final write
// belongs to the job, not the loop, so the caller adds it.
func governedWrites(o Options, window time.Duration) (int, error) {
	dir, err := os.MkdirTemp("", "ckpt-contrast-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l, err := loop.Open(loop.Config{
		Dir:           dir,
		UnitID:        "contrast",
		Bounds:        policy.Bounds{MinCopies: 1, NoUs: policy.NoUsNone, WithUs: window},
		Policy:        policy.Risk{},
		Now:           func() time.Time { return now },
		Window:        window,
		WriteEstimate: o.WriteCost,
		AlwaysDecide:  true,
	})
	if err != nil {
		return 0, err
	}

	writes := 0
	skip := 0
	for step := 1; step <= o.Steps; step++ {
		if skip > 0 {
			skip--
			continue
		}
		now = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(step) * o.Step)
		st, err := l.Round()
		if err != nil {
			return 0, err
		}
		// The final write is the job's own habit, not a decision: counting it here would charge the
		// policy for something the job would have done anyway.
		if st.Action != policy.ActionSave || step >= o.Steps {
			continue
		}
		writes++
		now = now.Add(o.WriteCost)
		if err := seal(dir, int64(step), now); err != nil {
			return 0, err
		}
		// Steps that pass while the write runs are not steps the loop can decide in: the job is
		// stopped at its save point for that long.
		skip = int(o.WriteCost / o.Step)
	}
	return writes, nil
}

// seal writes the smallest directory the loop will accept as a sealed copy.
func seal(dir string, step int64, at time.Time) error {
	d := filepath.Join(dir, "step-"+strconv.FormatInt(step, 10))
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(d, "rank-0.bin"), make([]byte, 1024), 0o644); err != nil {
		return err
	}
	raw, err := json.Marshal(map[string]any{
		"step":   step,
		"shards": []map[string]any{{"rank": 0, "bytes": 1024}},
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(d, "manifest.json"), raw, 0o644); err != nil {
		return err
	}
	entries, err := os.ReadDir(d)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.Chtimes(filepath.Join(d, e.Name()), at, at); err != nil {
			return err
		}
	}
	return os.Chtimes(d, at, at)
}

// Short renders a duration the way the table's column needs it: "2h", "30m", "40s".
func Short(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	switch {
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dh", int(d/(24*time.Hour))*24)
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	default:
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
}
