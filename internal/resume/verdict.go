package resume

import (
	"fmt"
	"strings"
)

// Reason is why a restore would get what it would get. It is a closed set, because a verdict is printed
// in a receipt and a receipt with free text cannot be compared across runs (decision 0026).
type Reason string

const (
	ReasonOK              Reason = "ok"                    // a full restore, world unchanged
	ReasonResharding      Reason = "resharding"            // a full restore across a world-size change
	ReasonStateAbsent     Reason = "state-absent"          // the file does not hold a state a full restore needs
	ReasonDeclaredPartial Reason = "declared-partial"      // the loader was told to skip a required state
	ReasonWorldChanged    Reason = "world-not-reshardable" // the world changed and the format cannot re-shard
	ReasonUnstated        Reason = "unstated"              // sealed, and nobody said what it holds
	ReasonInvalid         Reason = "invalid"               // the declaration cannot be believed
)

// required is what a full restore needs, in the catalogue's order: the model, and the state that makes
// continuing different from starting over (decision 0026). `scaler` is required only for mixed precision
// and `ema` only when the run keeps one, so neither blocks a resume.
var required = []string{"model", "optimizer", "scheduler", "rng", "dataloader"}

// Verdict is the whole answer to "can this be resumed". It carries the evidence it came from, because
// a verdict without its source is a number someone will eventually trust too much.
type Verdict struct {
	Reason     Reason
	Detail     string
	Generation int64
	Step       int64
	// World is what the writer ran under; Asked is what a restore would run under. Zero fields in
	// Asked mean "not asked", and the line then compares the checkpoint with itself.
	World World `json:"world"`
	Asked World `json:"asked"`
	// Missing is the required state the declaration lacks, in catalogue order. Empty when nothing is.
	Missing []string `json:"missing,omitempty"`
	// Declared is false when there was no declaration at all: the verdict is then about silence, not
	// about a claim, and the wording says so.
	Declared bool `json:"declared"`
}

// Resumable reports whether the verdict is one a job may act on. A resharding restore is resumable:
// it costs something, and the cost is named.
func (v Verdict) Resumable() bool { return v.Reason == ReasonOK || v.Reason == ReasonResharding }

// ExitCode keeps the product's three meanings: 0 yes, 1 checked and no, 2 cannot decide. A fourth code
// would be a compatibility change, and "we do not know" is not a failure of the copy.
func (v Verdict) ExitCode() int {
	switch {
	case v.Resumable():
		return 0
	case v.Reason == ReasonUnstated || v.Reason == ReasonInvalid:
		return 2
	default:
		return 1
	}
}

// Line is the sentence a receipt prints. Every branch names what the reader has to act on, and nothing
// is softened: "not resumable" is a fact about this checkpoint and this world, not a suggestion.
func (v Verdict) Line() string {
	want := v.Asked.Ranks
	if want == 0 {
		want = v.World.Ranks
	}
	switch v.Reason {
	case ReasonOK:
		return fmt.Sprintf("resumable (gen %d, step %d, world %d/%d, full state)",
			v.Generation, v.Step, v.World.Ranks, want)
	case ReasonResharding:
		return fmt.Sprintf("resumable with resharding (%d → %d ranks)", v.World.Ranks, want)
	case ReasonStateAbsent:
		also := ""
		if len(v.Missing) > 1 {
			also = fmt.Sprintf(" (also: %s)", strings.Join(v.Missing[1:], ", "))
		}
		return fmt.Sprintf("not resumable: %s state absent%s", v.Missing[0], also)
	case ReasonDeclaredPartial:
		return fmt.Sprintf("not resumable: declared partial (%s) — this is a fine-tune, not a resume", v.Detail)
	case ReasonWorldChanged:
		return fmt.Sprintf("not resumable: world changed %d → %d, and this format is not reshardable",
			v.World.Ranks, want)
	default: // unstated, invalid
		return "unknown: " + v.Detail
	}
}

// Judge turns a declaration into a verdict. The checks run in the order that makes the first refusal the
// answer — the same discipline as the classifier (0025): silence, then credibility, then the world, then
// the loader's intent, then what is missing. A later check is never asked about a copy an earlier one
// already refused, which is what keeps the reason meaningful.
//
// `asked` is the world a restore would run under. Zero fields mean "not asked", and a field the
// declaration does not state is treated as changed: an undeclared topology is not a promise of equality.
func Judge(decl File, declared bool, loadErr error, asked World) Verdict {
	if loadErr != nil {
		return Verdict{Reason: ReasonInvalid, Detail: loadErr.Error()}
	}
	if !declared {
		return Verdict{Reason: ReasonUnstated,
			Detail: "sealed, but nobody said what it holds"}
	}

	v := Verdict{
		Generation: decl.Generation,
		Step:       decl.Step,
		World:      decl.World,
		Asked:      asked,
		Declared:   true,
	}

	changed := worldChanged(decl.World, asked)
	if changed && !decl.World.Reshardable {
		v.Reason = ReasonWorldChanged
		return v
	}

	if skipped := firstRequired(decl.Partial); skipped != "" {
		v.Reason = ReasonDeclaredPartial
		v.Detail = skipped
		return v
	}

	v.Missing = missingRequired(decl.State)
	if len(v.Missing) > 0 {
		v.Reason = ReasonStateAbsent
		return v
	}

	if changed {
		v.Reason = ReasonResharding
		return v
	}
	v.Reason = ReasonOK
	return v
}

// worldChanged compares the fields the asker stated. A field the declaration left at 0 is treated as
// changed, because "the file did not say" is not "the same as yours".
func worldChanged(decl, asked World) bool {
	if asked.Ranks == 0 {
		return false
	}
	for _, e := range []struct{ asked, declared int }{
		{asked.Ranks, decl.Ranks}, {asked.TP, decl.TP}, {asked.PP, decl.PP}, {asked.DP, decl.DP},
	} {
		if e.asked == 0 {
			continue
		}
		if e.declared == 0 || e.declared != e.asked {
			return true
		}
	}
	return false
}

// firstRequired returns the first required state the loader was told to skip, in catalogue order.
func firstRequired(partial []string) string {
	for _, name := range required {
		for _, p := range partial {
			if p == name {
				return name
			}
		}
	}
	return ""
}

// missingRequired lists the required state the declaration does not hold, in catalogue order.
func missingRequired(s State) []string {
	var out []string
	for _, name := range required {
		if !s.holds(name) {
			out = append(out, name)
		}
	}
	return out
}
