package policy

// FieldStatus says what the risk algorithm needs from one contract field, so a new field cannot be
// added without someone deciding which of these it is. The alternative — a field nobody fills and
// nobody notices — is exactly what a frozen contract is supposed to prevent.
//
// The delivery side has its own table for the other question ("who produces this"), in
// internal/observe/delivery.go. The two are kept on the same field set by a test there, so a field
// cannot land in one and not the other.
type FieldStatus string

const (
	// FieldRequired: the policy cannot answer without it, and the delivery layer must fill it.
	FieldRequired FieldStatus = "required"
	// FieldOptional: the policy answers better with it, or the executor needs it; unknown is fine.
	FieldOptional FieldStatus = "optional"
	// FieldUnknownOK: V1 leaves it unknown on purpose; a filled value would change nothing today.
	FieldUnknownOK FieldStatus = "unknown-ok"
	// FieldDeprecated: the field belongs to the retired interval line (decision 0014). The
	// algorithm never reads it; filling it changes nothing and misleads the reader.
	FieldDeprecated FieldStatus = "deprecated"
)

// snapshotFieldStatus is the whole contract: eleven fields, each with a reader. The retired interval
// line (0014) asked for twenty more — step times, remaining time, delta share, full-write cost,
// streams, buffers, resume cost — and every one of them was filled by a measurement nobody read. They
// are gone rather than deprecated: a field that no code reads is a promise the contract does not keep,
// and `TestEveryContractFieldHasAnOwner` fails on any that comes back without a reader.
var snapshotFieldStatus = map[string]FieldStatus{
	// What the risk core reads.
	"UnitID":     FieldRequired,
	"Generation": FieldRequired,
	"Evidence":   FieldRequired,

	// Job-local facts, read directly rather than through a condition: one fact, one representation.
	"Copies":                   FieldRequired,
	"IncompleteRepairs":        FieldUnknownOK, // filled by the loop's completeness check, not by the scan
	"ConsecutiveWriteFailures": FieldRequired,
	"SaveInFlight":             FieldRequired,

	// The executor's inputs and the writer's facts.
	"SaveStartedAt":   FieldOptional,
	"SaveProgress":    FieldOptional,
	"Step":            FieldOptional,
	"CheckpointBytes": FieldOptional,
	"FreeBytes":       FieldOptional,
}

// boundFieldStatus is the other half of the contract. Retention, the storage limit and the resume
// mode were here for the optimiser and had no reader: keeping copies is the decision's KeepCopies,
// space is the executor's FreeBytes, and what a checkpoint contains is the producer's business.
var boundFieldStatus = map[string]FieldStatus{
	"NoUs":      FieldRequired,
	"NoUsEvery": FieldOptional,
	"WithUs":    FieldOptional,
	"MinCopies": FieldRequired,
}

// SnapshotFields returns what the policy needs from each snapshot field, in a copy a caller can
// read without being able to change the contract.
func SnapshotFields() map[string]FieldStatus {
	out := make(map[string]FieldStatus, len(snapshotFieldStatus))
	for name, status := range snapshotFieldStatus {
		out[name] = status
	}
	return out
}

// BoundsFields is the same for what the user commits to.
func BoundsFields() map[string]FieldStatus {
	out := make(map[string]FieldStatus, len(boundFieldStatus))
	for name, status := range boundFieldStatus {
		out[name] = status
	}
	return out
}

// RequiredSnapshotFields lists the fields without which the policy cannot answer at all. The
// delivery layer must fill these before anything else; the handoff sheet says in which order.
func RequiredSnapshotFields() []string {
	var out []string
	for name, status := range snapshotFieldStatus {
		if status == FieldRequired {
			out = append(out, name)
		}
	}
	return out
}
