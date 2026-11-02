package resume

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// GlueDir is where the job writes what only it knows (the plan lives here too).
const GlueDir = ".ckpt-governor"

// Glue file names. They are the job's, not the controller's: the controller writes the plan, the job
// reports what it did with it.
const (
	GlueResumedGeneration = "resumed-generation"
	GlueResumedStep       = "resumed-step"
	GlueLoadSeconds       = "load-seconds"
	GlueStep              = "step"
	GlueBoot              = "boot"
)

// AdoptionReason is why a job is where it is. Closed set, same reason as a verdict: it is printed.
type AdoptionReason string

const (
	AdoptAdopted  AdoptionReason = "adopted"
	AdoptCold     AdoptionReason = "cold"     // the job never said, and a seal existed
	AdoptUnused   AdoptionReason = "unused"   // the job adopted an older generation
	AdoptMismatch AdoptionReason = "mismatch" // the step matches, the generation does not
	AdoptNone     AdoptionReason = "none"     // there is nothing to adopt
	AdoptUnknown  AdoptionReason = "unknown"  // the report cannot be believed
)

// Adoption is the answer to "did it resume". It never says "resumed" without naming what was adopted:
// a sentence that cannot be checked is the kind of claim this product exists to refuse.
type Adoption struct {
	Reason AdoptionReason
	Detail string

	// SealedGeneration/SealedStep are the newest seal that existed when this job started.
	SealedGeneration int64
	SealedStep       int64
	// AdoptedGeneration/AdoptedStep are what the job reported it loaded. Zero when it said nothing.
	AdoptedGeneration int64
	AdoptedStep       int64
	// CurrentStep is the job's own step file, for the cold-start sentence.
	CurrentStep      int64
	CurrentStepKnown bool
	// LoadSeconds is the job's measured load time, as it wrote it. Empty when it did not measure.
	LoadSeconds string
	Reported    bool
}

// Line is the sentence a receipt prints.
func (a Adoption) Line() string {
	switch a.Reason {
	case AdoptAdopted:
		if a.LoadSeconds != "" {
			return fmt.Sprintf("adopted gen %d (step %d), load %ss", a.AdoptedGeneration, a.AdoptedStep, a.LoadSeconds)
		}
		return fmt.Sprintf("adopted gen %d (step %d)", a.AdoptedGeneration, a.AdoptedStep)
	case AdoptUnused:
		return fmt.Sprintf("adopted gen %d (step %d), newest sealed %d (step %d): %d steps on disk unused",
			a.AdoptedGeneration, a.AdoptedStep, a.SealedGeneration, a.SealedStep,
			a.SealedStep-a.AdoptedStep)
	case AdoptCold:
		step := "unknown"
		if a.CurrentStepKnown {
			step = strconv.FormatInt(a.CurrentStep, 10)
		}
		return fmt.Sprintf("not adopted: step %s, newest sealed %d", step, a.SealedStep)
	case AdoptMismatch:
		return fmt.Sprintf("adopted step %d from generation %d, newest sealed is %d",
			a.AdoptedStep, a.AdoptedGeneration, a.SealedGeneration)
	case AdoptNone:
		return "nothing to adopt: no sealed generation"
	default:
		return "unknown: " + a.Detail
	}
}

// ExitCode: 0 adopted (or nothing to adopt, which is not a failure), 1 checked and not adopted,
// 2 cannot decide. Same three meanings as everywhere else.
func (a Adoption) ExitCode() int {
	switch a.Reason {
	case AdoptAdopted, AdoptNone:
		return 0
	case AdoptUnknown:
		return 2
	default:
		return 1
	}
}

// Observe answers "did this job resume", from the job's own report and the seals that existed when it
// started. It reads only glue and manifests: no checkpoint bytes, no loader, no format knowledge.
func Observe(root string) (Adoption, error) {
	boot, _ := bootTime(root)
	sealed, ok, err := SealedBefore(root, boot)
	if err != nil {
		return Adoption{}, err
	}
	if !ok {
		return Adoption{Reason: AdoptNone}, nil
	}

	a := Adoption{SealedGeneration: sealed.Manifest.Generation, SealedStep: sealed.Manifest.Step}
	if cur, ok := intGlue(root, GlueStep); ok {
		a.CurrentStep, a.CurrentStepKnown = cur, true
	}
	if load, ok := stringGlue(root, GlueLoadSeconds); ok {
		a.LoadSeconds = load
	}

	stepText, ok := stringGlue(root, GlueResumedStep)
	if !ok {
		// The job never said what it loaded. That is not an error: it is what a cold start looks like
		// from outside, and the sentence says so rather than guessing.
		a.Reason = AdoptCold
		return a, nil
	}
	step, err := strconv.ParseInt(strings.TrimSpace(stepText), 10, 64)
	if err != nil {
		a.Reason = AdoptUnknown
		a.Detail = fmt.Sprintf("resumed-step is not a number: %q", stepText)
		return a, nil
	}
	a.AdoptedStep, a.Reported = step, true
	a.AdoptedGeneration, _ = intGlue(root, GlueResumedGeneration)

	switch {
	case step < a.SealedStep:
		a.Reason = AdoptUnused
	case step == a.SealedStep && a.AdoptedGeneration != a.SealedGeneration:
		a.Reason = AdoptMismatch
	case step == a.SealedStep:
		a.Reason = AdoptAdopted
	default:
		// Ahead of the newest seal that existed at boot. That is not an adoption of anything we can name,
		// and calling it one would be the "resumed" claim with no evidence behind it.
		a.Reason = AdoptUnknown
		a.Detail = fmt.Sprintf("adopted step %d is ahead of the newest seal (%d, step %d)",
			a.AdoptedStep, a.SealedGeneration, a.SealedStep)
	}
	return a, nil
}

// bootTime is when the job started, from the stamp it wrote at start. The stamp is base36 nanoseconds;
// a file that cannot be read falls back to its mtime, and a directory without one has no bound.
func bootTime(root string) (time.Time, bool) {
	path := filepath.Join(root, GlueDir, GlueBoot)
	raw, err := os.ReadFile(path)
	if err == nil {
		if ns, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 36, 64); err == nil && ns > 0 {
			return time.Unix(0, ns), true
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

func stringGlue(root, name string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(root, GlueDir, name))
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(raw)), true
}

func intGlue(root, name string) (int64, bool) {
	text, ok := stringGlue(root, name)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
