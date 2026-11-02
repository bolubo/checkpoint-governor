package policy

// The condition catalogue: what the producer can tell the algorithm about the world.
//
// Every entry is a named boolean with a fixed meaning. The algorithm never sees a percentage, a
// millisecond or a machine; it sees these names, each of which is 0, 1, "not applicable" or
// "cannot be read". The catalogue is frozen (decision 0013): adding one is
// a row here, a producer, and a test in the same commit.
//
// Three facts deliberately have no condition, because they are already typed fields that the
// algorithm reads directly and that it owns: a broken copy (`Copies`), consecutive write failures
// (`ConsecutiveWriteFailures`), and "a required condition could not be read" (the unknown state of
// each condition). One fact, one representation.
//
// Order is precedence: when several fire, the first one is the one the decision names. It follows
// consequence, not the alphabet, and a dead probe outranks everything it could have been hiding.
type Tier int

const (
	// TierUrgent: 1 means the work may be lost, so write now.
	TierUrgent Tier = iota
	// TierWarn: 1 means something is wrong but nothing is at risk of being lost.
	TierWarn
	// TierNote: 1 is informational; slower training is not a lost work.
	TierNote
)

func (t Tier) String() string {
	switch t {
	case TierUrgent:
		return "urgent"
	case TierWarn:
		return "warn"
	default:
		return "note"
	}
}

// MissingPolicy says what the absence of a reading means. The distinction is the whole reason the
// algorithm can stay environment-blind: a condition the environment does not have is inert, while
// one it has and cannot read is a risk.
type MissingPolicy int

const (
	// MissingInert: absent because the environment has no such thing (a CPU job has no GPU).
	MissingInert MissingPolicy = iota
	// MissingUrgent: present but unreadable is itself urgent.
	MissingUrgent
)

func (m MissingPolicy) String() string {
	if m == MissingUrgent {
		return "urgent"
	}
	return "inert"
}

// Condition is one row of the catalogue.
type Condition struct {
	// Name is both the contract name and the Evidence key.
	Name string
	// Tier decides what a 1 means.
	Tier Tier
	// Missing decides what an unreadable value means.
	Missing MissingPolicy
	// Why is one sentence a person can read in a decision.
	Why string
}

// conditionCatalog is the frozen set. The order is the precedence order.
var conditionCatalog = []Condition{
	{Name: "watch.broken", Tier: TierUrgent, Missing: MissingInert,
		Why: "the probe that feeds the conditions is dead, so we cannot see anything"},
	{Name: "risk.preempt", Tier: TierUrgent, Missing: MissingUrgent,
		Why: "higher-priority or more urgent work can take these resources"},
	{Name: "risk.shutdown", Tier: TierUrgent, Missing: MissingUrgent,
		Why: "the job, container or node is being closed"},
	{Name: "store.low-space", Tier: TierUrgent, Missing: MissingUrgent,
		Why: "free space is below two checkpoints, so this write may be the last that can complete"},
	{Name: "host.memory-pressure", Tier: TierUrgent, Missing: MissingUrgent,
		Why: "memory is at the eviction line"},
	{Name: "host.gpu-fault", Tier: TierUrgent, Missing: MissingUrgent,
		Why: "a GPU is failing"},
	{Name: "job.ending", Tier: TierUrgent, Missing: MissingInert,
		Why: "training is finishing or a stop was requested"},
	// The inference sits last among the urgent conditions on purpose: everything above it is a
	// measurement or an announcement, and when both fire the decision names the certain one. It can
	// never outrank `risk.preempt`, which is the platform telling us rather than us guessing.
	{Name: "risk.eviction-likely", Tier: TierUrgent, Missing: MissingUrgent,
		Why: "the node is likely to need this job's room soon"},

	{Name: "store.blocked", Tier: TierWarn, Missing: MissingInert,
		Why: "the write target refuses right now, so a write cannot help"},
	{Name: "store.slow", Tier: TierWarn, Missing: MissingInert,
		Why: "writes are much slower than this deployment's normal"},
	{Name: "host.gpu-hot", Tier: TierNote, Missing: MissingInert,
		Why: "the GPU is throttling"},
	{Name: "host.cpu-throttled", Tier: TierNote, Missing: MissingInert,
		Why: "CPU quota is throttling the job"},
	{Name: "job.step-slow", Tier: TierNote, Missing: MissingInert,
		Why: "step time is well above its normal"},
	{Name: "net.long-outage", Tier: TierNote, Missing: MissingInert,
		Why: "the network has been down long enough to matter"},
}

// Conditions returns the catalogue in a copy, so a caller can read it and cannot change it.
func Conditions() []Condition {
	out := make([]Condition, len(conditionCatalog))
	copy(out, conditionCatalog)
	return out
}

// ConditionNamed returns one row of the catalogue.
func ConditionNamed(name string) (Condition, bool) {
	for _, c := range conditionCatalog {
		if c.Name == name {
			return c, true
		}
	}
	return Condition{}, false
}
