package adapters

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Glue is everything a job can tell the controller about itself, read in one place.
//
// It exists because round 2 needs these as *facts for the executor* (headroom before the first seal,
// the duration of one write, and the two-phase exit), not as conditions: none of these is a 0/1 that
// should trigger a write on its own. Keeping them here rather than in a probe is the same discipline
// as `Copies`: one fact, one representation, and the one that has a consumer decides the shape.
//
// This is the only reader of the glue files. The executor (`loop`) and the CLI both call it, so the
// two cannot drift into disagreeing about what a file means — which is exactly what would happen
// with a second parser, and it would happen silently.
//
// One fact, one file, is also why there is no `stop-requested` file: `.ckpt-governor/stopping` (the
// file the job writes on SIGTERM) **is** the stop request, and a second name for it would let the
// two disagree.
type Glue struct {
	// ExpectedBytes is what the job says its next checkpoint will weigh. The job knows its own
	// parameter count; we do not, and guessing is how a headroom check becomes a lie.
	ExpectedBytes int64
	HasExpected   bool

	// LastSaveSeconds is how long the job's own previous save took. It is the second-best estimate of
	// one write, after the measured p90 and before size over probed throughput.
	LastSaveSeconds float64
	HasLastSave     bool

	// StopRequested is `.ckpt-governor/stopping` being present.
	StopRequested bool

	// Ending is the file "ending": training is over, so whatever exists now is the work that matters.
	// It is also the honest moment to answer the stop handshake.
	Ending bool

	// MayExit is the controller's answer to the handshake: the job may leave.
	MayExit bool

	// SecondsLeft is how much of the user's grace period the job thinks remains. The job owns the
	// deadline because the job was given it; the controller must not invent a second one.
	SecondsLeft int64
	HasSeconds  bool

	// Members is each rank's own step, read from `ranks/<rank>/step`. Empty when that directory
	// does not exist: that is the single-writer path, and every answer stays exactly as it was.
	// The minimum is the real progress (the slowest rank is the unit), and the maximum minus it is
	// the spread, which is reported and never used to suppress a write.
	//
	// It is `ranks/`, not `members/`: `members` is already the launcher's roster file, and one name
	// cannot be both a file and a directory.
	Members []MemberStep
}

// MemberStep is one rank's own view of where it is.
type MemberStep struct {
	Rank    int
	Step    int64
	HasStep bool
}

// Progress is the slowest reporting rank's step: a unit has not reached a step until every member
// has. Empty members mean "not a gang", and the caller falls back to the team-level `step`.
func (g Glue) Progress() (int64, bool) {
	var best int64
	found := false
	for _, m := range g.Members {
		if !m.HasStep {
			continue
		}
		if !found || m.Step < best {
			best, found = m.Step, true
		}
	}
	return best, found
}

// Spread is max − min over the ranks that reported. It is a diagnostic: a wide spread is worth
// saying, and it is never a reason to skip a write (0021).
func (g Glue) Spread() int64 {
	var min, max int64
	found := false
	for _, m := range g.Members {
		if !m.HasStep {
			continue
		}
		if !found {
			min, max, found = m.Step, m.Step, true
			continue
		}
		if m.Step < min {
			min = m.Step
		}
		if m.Step > max {
			max = m.Step
		}
	}
	return max - min
}

// Silent lists the ranks that have a member directory but no step: a rank that started and has not
// reported is worth naming, and it never stops the others from writing.
func (g Glue) Silent() []int {
	var out []int
	for _, m := range g.Members {
		if !m.HasStep {
			out = append(out, m.Rank)
		}
	}
	return out
}

// ReadGlue reads what the job left behind. A missing file is zero values with its Has flag false:
// a job that never wrote these is a job that never had a stop, not one hiding something.
func ReadGlue(dir string) Glue {
	var g Glue
	base := filepath.Join(dir, ".ckpt-governor")
	if n, ok := readNumber(base, "expected-bytes"); ok {
		g.ExpectedBytes, g.HasExpected = n, true
	}
	if f, ok := readDecimal(base, "last-save-seconds"); ok {
		g.LastSaveSeconds, g.HasLastSave = f, true
	}
	if _, err := os.Stat(filepath.Join(base, "stopping")); err == nil {
		g.StopRequested = true
	}
	if _, err := os.Stat(filepath.Join(base, "ending")); err == nil {
		g.Ending = true
	}
	if _, err := os.Stat(filepath.Join(base, "may-exit")); err == nil {
		g.MayExit = true
	}
	if n, ok := readNumber(base, "seconds-left"); ok {
		g.SecondsLeft, g.HasSeconds = n, true
	}
	g.Members = readMembers(base)
	return g
}

// readMembers reads `ranks/<rank>/step`. A directory with no step is a silent rank, not an absent
// one: it was listed by the launcher and has not said where it is, which is different from a unit
// that has only one process.
func readMembers(base string) []MemberStep {
	entries, err := os.ReadDir(filepath.Join(base, "ranks"))
	if err != nil {
		return nil
	}
	var out []MemberStep
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rank, convErr := strconv.Atoi(e.Name())
		if convErr != nil || rank < 0 {
			continue
		}
		m := MemberStep{Rank: rank}
		if n, ok := readNumber(filepath.Join(base, "ranks", e.Name()), "step"); ok {
			m.Step, m.HasStep = n, true
		}
		out = append(out, m)
	}
	return out
}

func readNumber(base, name string) (int64, bool) {
	raw, err := os.ReadFile(filepath.Join(base, name))
	if err != nil {
		return 0, false
	}
	n, convErr := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if convErr != nil {
		return 0, false
	}
	return n, true
}

// readDecimal reads the files that carry fractions (seconds), so a job that reports 13.7 seconds is
// not silently rounded to 13 and then treated as exact.
func readDecimal(base, name string) (float64, bool) {
	raw, err := os.ReadFile(filepath.Join(base, name))
	if err != nil {
		return 0, false
	}
	f, convErr := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
	if convErr != nil {
		return 0, false
	}
	return f, true
}
