package observe

// Delivery says where one contract field comes from and who acts on it.
//
// The algorithm's table (policy.SnapshotFields) answers "does the policy need it". This one answers
// the other half — "who produces it, and who consumes it" — and a field with no consumer is a field
// that will rot. The two tables are kept on the same field set by a test, so a field cannot land in
// one and not the other.
type FieldSource struct {
	// Where is who produces the value: a syscall, a file the job writes, the ledger, config.
	Where string
	// Consumer is who acts on it: policy, executor, job, human, or a "+" of those.
	Consumer string
}

// The status of each field (required, optional, deprecated) is deliberately not repeated here: it
// belongs to the algorithm, and duplicating it would let the two drift.
var deliverySheet = map[string]FieldSource{
	// What the risk core reads.
	"UnitID":                   {Where: "config", Consumer: "policy+job"},
	"Generation":               {Where: "ledger", Consumer: "policy+job"},
	"Evidence":                 {Where: "probes", Consumer: "policy"},
	"Copies":                   {Where: "scan + the copy's declaration", Consumer: "policy+executor"},
	"IncompleteRepairs":        {Where: "ledger", Consumer: "policy"},
	"ConsecutiveWriteFailures": {Where: "ledger", Consumer: "policy"},
	"SaveInFlight":             {Where: "scan", Consumer: "policy"},

	// The executor's inputs and the writer's facts.
	"SaveStartedAt":   {Where: "ledger", Consumer: "executor"},
	"SaveProgress":    {Where: "job file", Consumer: "executor+human"},
	"Step":            {Where: "job file, else the newest seal", Consumer: "job"},
	"CheckpointBytes": {Where: "scan", Consumer: "executor"},
	"FreeBytes":       {Where: "statfs", Consumer: "executor"},
}

// Delivery returns the sheet, for a test or a person.
func Delivery() map[string]FieldSource {
	out := make(map[string]FieldSource, len(deliverySheet))
	for k, v := range deliverySheet {
		out[k] = v
	}
	return out
}
