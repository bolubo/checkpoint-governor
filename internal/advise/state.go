// Package advise turns a decision into the one file a training job reads.
//
// It owns the published file (the contract) and the private ledger, and nothing else: no
// measurement, no arithmetic. The file has to be byte-identical for identical decisions, because a
// rewrite changes the mtime, wakes the job and makes the plan look newer than it is.
package advise

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// BaseDir is the directory inside a checkpoint directory that holds both files.
const BaseDir = ".ckpt-governor"

// stateName is the published file: the contract with the job and the platform.
const stateName = "state.json"

// stateFile is the published contract: the fields are listed in docs/decisions/0017-two-numbers.md
// and consumed by cmd/ckpttrainer. A field that no reader uses is a second thing to keep in sync.
//
// SaveNow is deliberately absent: it is true exactly when Action is save or repair, and a second
// copy of one fact is a second thing to keep in sync. The job reads Action.
type stateFile struct {
	Schema     int    `json:"schema"`
	Epoch      int64  `json:"epoch"`
	ExpiresAt  int64  `json:"expiresAt"`
	UnitID     string `json:"unitId"`
	Generation int64  `json:"generation"`
	Action     string `json:"action"`
	Reason     string `json:"reason"`
	// RequestID is the identity of the request this plan makes; empty when the plan asks for nothing.
	// The job acts on a request at most once, keyed by this field, and the executor settles the write
	// that answers it by the same id — so "who asked for this checkpoint" is a fact, not an inference
	// (design-v1 §5).
	RequestID string `json:"requestId"`
	// BaselineSeconds is the user's `no-us`: the job's own period if the governor is gone. Zero with
	// BaselineNone true means "the user would not have written at all". It has no effect while the
	// governor answers; it is the fallback and the baseline of the saving.
	BaselineSeconds int64 `json:"baselineSeconds"`
	BaselineNone    bool  `json:"baselineNone"`
	// WindowSeconds is the user's `with-us`: the longest a checkpoint may stay stale while the
	// governor is alive. Zero means exceptions only.
	WindowSeconds int64 `json:"windowSeconds"`
	CopiesToWrite int   `json:"copiesToWrite"`
	// What the next write should produce (V1). Empty kind means a full snapshot, which is what a
	// producer that cannot write anything else reads.
	Kind             string   `json:"kind"`
	KeepCopies       int      `json:"keepCopies"`
	OldestStepToKeep int64    `json:"oldestStepToKeep"`
	Why              string   `json:"why"`
	Version          string   `json:"version"`
	Missing          []string `json:"missing"`
	// Reasons is which fact caused the write, in the algorithm's own vocabulary. The contract says a
	// consumer must be able to read it (0013); a plan that carries only a sentence forces every
	// reader to parse prose.
	Reasons []stateReason `json:"reasons,omitempty"`
	// Sensitivity says what would have changed the answer. It is for a person deciding whether to
	// trust the plan, so it travels with the plan.
	Sensitivity []stateSensitivity `json:"sensitivity,omitempty"`
}

type stateReason struct {
	Code     string             `json:"code"`
	Values   map[string]float64 `json:"values,omitempty"`
	Evidence []string           `json:"evidence,omitempty"`
}

type stateSensitivity struct {
	Field     string  `json:"field"`
	Threshold float64 `json:"threshold"`
	Direction string  `json:"direction"`
	WouldBe   string  `json:"wouldBe"`
}

// SchemaVersion is the version of the published file. It changes only with a decision record.
//
// 2: `requestId`, `baselineSeconds`/`baselineNone` and `windowSeconds` replace
// `fallbackIntervalSeconds`; the expiry means "a live governor exists" rather than a period
// (docs/design.md §5).
const SchemaVersion = 2

// stateOf builds the published structure. It takes expiresAt rather than a clock so that the bytes
// are a function of its arguments, which is what makes the idempotence check exact.
func stateOf(d policy.Decision, epoch int64, expiresAt time.Time) stateFile {
	missing := d.Missing
	if missing == nil {
		// Null and [] are different sentences to a reader: "nothing was missing" versus
		// "the writer did not say". The contract prints an empty array.
		missing = []string{}
	}
	return stateFile{
		Schema:           SchemaVersion,
		Epoch:            epoch,
		ExpiresAt:        expiresAt.Unix(),
		UnitID:           d.UnitID,
		Generation:       d.Generation,
		Action:           string(d.Action),
		Reason:           string(d.Reason),
		RequestID:        d.RequestID,
		BaselineSeconds:  d.BaselineSeconds,
		BaselineNone:     d.BaselineNone,
		WindowSeconds:    d.WindowSeconds,
		CopiesToWrite:    d.CopiesToWrite,
		Kind:             string(d.Kind),
		KeepCopies:       d.KeepCopies,
		OldestStepToKeep: d.OldestStepToKeep,
		Why:              d.Why,
		Version:          d.Version,
		Missing:          missing,
		Reasons:          reasonsOf(d),
		Sensitivity:      sensitivityOf(d),
	}
}

func reasonsOf(d policy.Decision) []stateReason {
	if len(d.Reasons) == 0 {
		return nil
	}
	out := make([]stateReason, 0, len(d.Reasons))
	for _, r := range d.Reasons {
		out = append(out, stateReason{Code: r.Code, Values: r.Values, Evidence: r.Evidence})
	}
	return out
}

func sensitivityOf(d policy.Decision) []stateSensitivity {
	if len(d.Sensitivity) == 0 {
		return nil
	}
	out := make([]stateSensitivity, 0, len(d.Sensitivity))
	for _, s := range d.Sensitivity {
		out = append(out, stateSensitivity{
			Field: s.Field, Threshold: s.Threshold, Direction: s.Direction, WouldBe: s.WouldBe,
		})
	}
	return out
}

// encode renders the file.
func encode(d policy.Decision, epoch int64, expiresAt time.Time) ([]byte, error) {
	return json.MarshalIndent(stateOf(d, epoch, expiresAt), "", "  ")
}

// Published is the file as read back: the plan, the epoch that produced it, and when it was written.
//
// PublishedAt is the file's modification time rather than a field in it: the contract has no
// "written at" key, and the file system already knows.
type Published struct {
	Epoch       int64
	Decision    policy.Decision
	PublishedAt time.Time
	// ExpiresAt is when the file stops being authoritative: after it, the job's rule is "the governor
	// is gone". It is read from the file rather than computed here, so a test and a view agree with
	// what the job acts on.
	ExpiresAt time.Time
}

// ReadPublished reads the published file, reporting false when there is none.
func ReadPublished(dir string) (Published, bool) {
	path := filepath.Join(dir, BaseDir, stateName)
	s, ok := readState(path)
	if !ok {
		return Published{}, false
	}
	p := Published{Epoch: s.Epoch, Decision: decode(s), ExpiresAt: time.Unix(s.ExpiresAt, 0)}
	if info, err := os.Stat(path); err == nil {
		p.PublishedAt = info.ModTime()
	}
	return p, true
}

// LastDecision reads the published plan back, for the snapshot's Previous field and for the human
// view. A missing or unreadable file is "no previous decision", not an error.
func LastDecision(dir string) (policy.Decision, bool) {
	s, ok := readState(filepath.Join(dir, BaseDir, stateName))
	if !ok {
		return policy.Decision{}, false
	}
	return decode(s), true
}

func decode(s stateFile) policy.Decision {
	d := policy.Decision{
		UnitID:           s.UnitID,
		Generation:       s.Generation,
		Action:           policy.Action(s.Action),
		Reason:           policy.Reason(s.Reason),
		SaveNow:          s.Action == string(policy.ActionSave) || s.Action == string(policy.ActionRepair),
		RequestID:        s.RequestID,
		BaselineSeconds:  s.BaselineSeconds,
		BaselineNone:     s.BaselineNone,
		WindowSeconds:    s.WindowSeconds,
		CopiesToWrite:    s.CopiesToWrite,
		Kind:             policy.WriteKind(s.Kind),
		KeepCopies:       s.KeepCopies,
		OldestStepToKeep: s.OldestStepToKeep,
		Missing:          s.Missing,
		Why:              s.Why,
		Version:          s.Version,
		Reasons:          reasonsFrom(s.Reasons),
		Sensitivity:      sensitivityFrom(s.Sensitivity),
	}
	return d
}

func reasonsFrom(in []stateReason) []policy.ReasonEntry {
	if len(in) == 0 {
		return nil
	}
	out := make([]policy.ReasonEntry, 0, len(in))
	for _, r := range in {
		out = append(out, policy.ReasonEntry{Code: r.Code, Values: r.Values, Evidence: r.Evidence})
	}
	return out
}

func sensitivityFrom(in []stateSensitivity) []policy.SensitivityEntry {
	if len(in) == 0 {
		return nil
	}
	out := make([]policy.SensitivityEntry, 0, len(in))
	for _, s := range in {
		out = append(out, policy.SensitivityEntry{
			Field: s.Field, Threshold: s.Threshold, Direction: s.Direction, WouldBe: s.WouldBe,
		})
	}
	return out
}

// stamp renders an absolute time; an empty string means "now", which is what a zero stamp says.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
