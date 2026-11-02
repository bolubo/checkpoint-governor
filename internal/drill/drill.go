package drill

import (
	"context"
	"fmt"
	"time"
)

// Reason is why a drill ended the way it did. Closed set: the receipt prints it.
type Reason string

const (
	ReasonRestorable Reason = "restorable" // the loader took exactly one step from the seal
	ReasonWrongStep  Reason = "wrong-step" // the loader ran, and did not start from the seal
	ReasonTimeout    Reason = "timeout"    // the loader did not finish
	ReasonFailed     Reason = "failed"     // the loader exited non-zero
	ReasonMutated    Reason = "mutated"    // the loader changed the checkpoint: refused
)

// Report is what one drill has to say. `Mutated` is not a warning: a drill that changed the evidence is
// not a test of the evidence (decision 0026).
type Report struct {
	Reason       Reason
	Detail       string
	DeclaredStep int64
	ReportedStep int64
	Seconds      float64
}

// Restorable is true only for the one reason that earned it.
func (r Report) Restorable() bool { return r.Reason == ReasonRestorable }

// ExitCode: 0 restorable, 1 ran and did not, 2 refused (a mutated tree, or nothing to drill).
func (r Report) ExitCode() int {
	switch r.Reason {
	case ReasonRestorable:
		return 0
	case ReasonMutated:
		return 2
	default:
		return 1
	}
}

// Line is the sentence a receipt prints.
func (r Report) Line() string {
	switch r.Reason {
	case ReasonRestorable:
		return fmt.Sprintf("resume: ok — step %d → %d; %.1fs", r.DeclaredStep, r.ReportedStep, r.Seconds)
	case ReasonWrongStep:
		return fmt.Sprintf("not restorable: the loader reported step %d (declared %d)", r.ReportedStep, r.DeclaredStep)
	case ReasonTimeout:
		return "drill failed: " + r.Detail
	case ReasonMutated:
		return "drill refused: " + r.Detail
	default:
		return "drill failed: " + r.Detail
	}
}

// Do runs one drill: fingerprint the tree, run the loader once, fingerprint again, and decide.
//
// The order matters. The second fingerprint happens before the step is judged, so a loader that
// "succeeded" by writing the checkpoint it was supposed to read is a refusal rather than a pass.
func Do(ctx context.Context, root, genDir string, generation, declaredStep int64, loader string,
	timeout time.Duration) (Report, error) {
	before, err := Take(root)
	if err != nil {
		return Report{}, err
	}

	res, err := Run(ctx, loader, genDir, generation, timeout)
	if err != nil {
		return Report{}, err
	}

	after, err := Take(root)
	if err != nil {
		return Report{}, err
	}
	if where, changed := before.Difference(after); changed {
		return Report{Reason: ReasonMutated,
			Detail:       "the checkpoint changed during the drill (" + where + ")",
			DeclaredStep: declaredStep}, nil
	}

	rep := Report{Reason: ReasonRestorable, DeclaredStep: declaredStep, ReportedStep: res.Step,
		Seconds: res.Duration.Seconds()}
	switch {
	case res.TimedOut:
		rep.Reason = ReasonTimeout
		rep.Detail = fmt.Sprintf("the loader did not finish within %s", timeout)
	case res.ExitCode != 0:
		rep.Reason = ReasonFailed
		rep.Detail = fmt.Sprintf("the loader exited %d", res.ExitCode)
		if line := LastLine(res.Output); line != "" {
			rep.Detail += ": " + line
		}
	case !res.StepKnown:
		rep.Reason = ReasonWrongStep
		rep.ReportedStep = 0
		rep.Detail = "the loader printed no step=<n> line"
	case res.Step != declaredStep+1:
		rep.Reason = ReasonWrongStep
	}
	return rep, nil
}
