package verify

import (
	"fmt"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// Action is what the controller does next. Five of them, and each one has exactly one reason to exist.
type Action string

const (
	// ActionDone: the copy is verified. Stop.
	ActionDone Action = "done"
	// ActionWait: the write is still in flight, or the generation we asked for has not landed yet.
	ActionWait Action = "wait"
	// ActionVerify: the structure agrees but nobody has read the bytes. This is not a rewrite; it is the
	// check we still owe.
	ActionVerify Action = "verify"
	// ActionRewrite: ask again, as a new generation.
	ActionRewrite Action = "rewrite"
	// ActionGiveUp: stop asking. Either it cannot fit, or the budget is spent, and both are things to
	// report rather than to retry.
	ActionGiveUp Action = "give-up"
)

// Budget bounds how long and how often we ask. A rewrite loop without one turns a single bad checkpoint
// into a busy volume, which is a worse failure than the one it was trying to fix.
type Budget struct {
	Attempts int
	Window   time.Duration
}

// Progress is what the controller remembers between rounds. It is passed in rather than held here, so the
// decision stays a pure function and can be tested without a clock or a directory.
type Progress struct {
	Attempts int
	FirstAt  time.Time
	// CannotFit is set by the caller when the volume cannot hold another generation. We never discover
	// this from a manifest: it is a fact about the disk.
	CannotFit bool
}

// Decision carries the next generation as well as the action, because "rewrite" without a number is an
// instruction a writer cannot follow idempotently.
type Decision struct {
	Action     Action
	Generation int64
	Detail     string
}

// Next decides what to do about one generation.
//
// The rule that matters: **a rewrite is a new generation, never a retry of the old one.** A slow writer
// finishing generation 7 after we asked for 8 must not be able to satisfy the newer request with the older
// copy, and the only way to make that true is for the request to carry a number that only moves forward.
func Next(v Verdict, want int64, p Progress, b Budget, now time.Time) Decision {
	if v.State == policy.CopyVerified {
		return Decision{Action: ActionDone, Generation: v.Generation,
			Detail: fmt.Sprintf("generation %d verified", v.Generation)}
	}
	if v.State == policy.CopySealed {
		// The structure holds. What is owed is a read, not a write — and saying "rewrite" here would spend
		// a write to answer a question a read can answer.
		return Decision{Action: ActionVerify, Generation: v.Generation,
			Detail: "structure agrees; the content check is still owed"}
	}

	switch v.Reason {
	case ReasonWaiting:
		return Decision{Action: ActionWait, Generation: v.Generation,
			Detail: "the write is in flight"}
	case ReasonStale:
		// The generation on disk is older than the one asked for. Rewriting would ask for a third; the
		// request for `want` still stands, so wait for it.
		return Decision{Action: ActionWait, Generation: want,
			Detail: fmt.Sprintf("generation %d is older than %d, which is still owed", v.Generation, want)}
	}

	// Everything else is a copy that exists and is not good enough: missing, expired, short, hash, invalid.
	if p.CannotFit {
		return Decision{Action: ActionGiveUp, Generation: want,
			Detail: "the volume cannot hold another generation: rewriting would only fill it"}
	}
	if b.Attempts > 0 && p.Attempts >= b.Attempts {
		return Decision{Action: ActionGiveUp, Generation: want,
			Detail: fmt.Sprintf("%d attempts used, the budget was %d", p.Attempts, b.Attempts)}
	}
	if b.Window > 0 && !p.FirstAt.IsZero() && now.Sub(p.FirstAt) > b.Window {
		return Decision{Action: ActionGiveUp, Generation: want,
			Detail: fmt.Sprintf("%s spent asking, the budget was %s", now.Sub(p.FirstAt).Round(time.Second), b.Window)}
	}

	return Decision{Action: ActionRewrite, Generation: nextGeneration(v.Generation, want),
		Detail: rewriteReason(v)}
}

// nextGeneration never goes backwards, and never repeats: the number is what makes a late write
// distinguishable from an answer.
func nextGeneration(sealed, want int64) int64 {
	g := want
	if sealed > g {
		g = sealed
	}
	return g + 1
}

// rewriteReason says what is wrong in the writer's terms, because a request it cannot act on is a retry
// dressed as a diagnosis.
func rewriteReason(v Verdict) string {
	switch v.Reason {
	case ReasonShort:
		return "the copy is short: " + v.Detail
	case ReasonHash:
		return "the copy does not match its manifest: " + v.Detail
	case ReasonExpired:
		return "no manifest arrived before the deadline: " + v.Detail
	case ReasonMissing:
		return "nothing landed at the requested generation: " + v.Detail
	case ReasonInvalid:
		return "the manifest cannot be believed: " + v.Detail
	default:
		return string(v.Reason) + ": " + v.Detail
	}
}
