package policy

import "time"

// NoUsMode says what the job does when the governor is not there to answer. It is the user's own
// number (`no-us`), and it has three states. The middle one matters: "not answered yet" is not "no
// writes for ever", and an executor must never infer the latter from an empty value
// (docs/decisions/0015-risk-core.md §4, docs/design.md §6).
type NoUsMode string

const (
	// NoUsUnset means the user has not answered. The executor refuses to start until they do,
	// because both the fallback and the saving are computed from this number.
	NoUsUnset NoUsMode = ""
	// NoUsNone means the user accepts that if we are gone, nothing is written: he would not have
	// written either.
	NoUsNone NoUsMode = "none"
	// NoUsEvery means the job writes on its own timer every Bounds.NoUsEvery while we are gone.
	NoUsEvery NoUsMode = "every"
)

// Bounds are the numbers the user sets. They are the only configuration the algorithm sees.
//
// Retention, the storage limit and the resume mode used to live here too. They were configuration the
// risk core never read: how many copies to keep is the decision's KeepCopies, how much space is left
// is the executor's FreeBytes, and what a checkpoint must contain is the producer's business.
type Bounds struct {
	// MinCopies is the smallest number of copies that must exist. Values below 1 mean 1.
	MinCopies int
	// NoUs is what the job does when the governor is gone: unset (refuse to start), none (the user
	// would not have written anyway), or every (the job's own timer runs, and NoUsEvery says how
	// often). It has no effect while the governor is alive.
	NoUs NoUsMode
	// NoUsEvery is the job's own period when NoUs is NoUsEvery.
	NoUsEvery time.Duration
	// WithUs is the longest a checkpoint may stay stale while the governor is alive. Zero means
	// exceptions only. It is a deadman window: every write that seals restarts it, so it bounds
	// staleness without becoming a schedule (docs/design.md §3).
	WithUs time.Duration
}

// ReasonEntry is one machine-readable step of the explanation: a code, the numbers that made it
// fire, and which measurements it read. Nothing parses Why; this is what a UI renders.
type ReasonEntry struct {
	Code     string
	Values   map[string]float64
	Evidence []string
}

// SensitivityEntry says what would change the answer: a field, the threshold at which it changes,
// which side of the threshold matters, and what the decision would become.
type SensitivityEntry struct {
	Field     string
	Threshold float64
	Direction string
	WouldBe   string
}

// CopyState is the state of one checkpoint copy on disk.
type CopyState string

const (
	// CopySealed means every part arrived and the manifest is sealed, but the content has not
	// been read back and checked.
	CopySealed CopyState = "sealed"
	// CopyVerified means the content was read back and matches the manifest.
	CopyVerified CopyState = "verified"
	// CopyBroken means the structure or the content does not match the manifest.
	CopyBroken CopyState = "broken"
	// CopyUnverified means the copy is not sealed yet, or no check has run.
	CopyUnverified CopyState = "unverified"
)

// Copy describes one checkpoint copy on disk.
type Copy struct {
	Step      int64
	Bytes     int64
	State     CopyState
	CreatedAt time.Time
}

// WriteKind is what a checkpoint write produces. Empty means full, so a producer that only writes
// full snapshots keeps the old behaviour without knowing this type exists.
type WriteKind string

const (
	// WriteFull is a complete snapshot: it restores the job by itself.
	WriteFull WriteKind = "full"
	// WriteDelta is everything that changed since the last checkpoint. It restores only together
	// with the full snapshot below it in the chain.
	WriteDelta WriteKind = "delta"
)

// Snapshot is everything the algorithm may look at. It is produced by the implementation layer
// and must be complete: a missing measurement is written as unknown, never as zero.
type Snapshot struct {
	// UnitID is opaque to the algorithm. The implementation decides what one unit is: a single
	// pod, a gang, or a batch job. The algorithm never sees that shape.
	UnitID string
	// Generation increases when the unit restarts. A decision produced for an older generation
	// must not be applied to a newer one.
	Generation int64

	// Step is the training step the unit has reached.
	Step int64

	// SaveInFlight says that a write is running right now.
	SaveInFlight bool
	// SaveStartedAt is when the running write started. Zero means unknown.
	SaveStartedAt time.Time
	// SaveProgress is the writer's own report on the running write, in [0, 1].
	// A negative value means unknown.
	SaveProgress float64
	// CheckpointBytes is the size of one checkpoint. Zero means unknown.
	CheckpointBytes int64

	// FreeBytes is the free space of the checkpoint volume. A negative value means unknown.
	FreeBytes int64

	// Copies are the copies currently on disk, in any order.
	Copies []Copy
	// ConsecutiveWriteFailures is how many writes failed in a row without a success in between.
	ConsecutiveWriteFailures int

	// Evidence carries the conditions, as booleans at the border. A missing key is absent (the
	// environment has no such thing), never zero; an unreadable one is an Unknown observation.
	Evidence map[string]Observation
}

// Kind is the type of one observation.
type Kind string

const (
	Number  Kind = "number"
	Boolean Kind = "bool"
	Text    Kind = "text"
	Unknown Kind = "unknown"
)

// Observation is one optional signal supplied by the implementation layer.
//
// It carries the producer (Source) and the moment it was taken (At) so that a decision can be
// replayed and audited without knowing which adapter produced it.
type Observation struct {
	Kind   Kind
	Number float64
	Bool   bool
	Text   string
	At     time.Time
	Source string
}

// Action is what the implementation should do at this save point.
type Action string

const (
	// ActionSave means write a new checkpoint now.
	ActionSave Action = "save"
	// ActionRepair means the newest checkpoint is broken and must be written again now. The
	// implementation should remove or quarantine the broken copy first.
	ActionRepair Action = "repair"
	// ActionSkip means do not write at this save point; the next decision is due at NextCheck.
	// An implementation that only adds writes (and never suppresses the job's own cadence) may
	// ignore this action.
	ActionSkip Action = "skip"
	// ActionHold means a write is already running; do nothing.
	ActionHold Action = "hold"
	// ActionInfeasible means the bounds cannot be met; the implementation must report it.
	ActionInfeasible Action = "infeasible"
	// ActionUnknown means a measurement is missing; the implementation must use its fallback.
	ActionUnknown Action = "unknown"
)

// Reason is the machine-readable cause of an action. Why is for people; Reason is for code.
type Reason string

const (
	ReasonSchedule   Reason = "schedule"
	ReasonFirst      Reason = "first-checkpoint"
	ReasonRepair     Reason = "repair"
	ReasonCapacity   Reason = "capacity"
	ReasonEnd        Reason = "end-of-training"
	ReasonInfeasible Reason = "infeasible-bounds"
	ReasonUnknown    Reason = "missing-measurement"
	// ReasonRisk means a condition from the catalogue fired: the work may be lost, write now.
	ReasonRisk Reason = "risk"
	// ReasonCalm means no condition fired: do not write.
	ReasonCalm Reason = "calm"
)

// Decision is what the algorithm returns. Every field the implementation needs is machine
// readable; Why is for a human and must never be parsed.
type Decision struct {
	// UnitID and Generation are copied from the snapshot. A decision is only valid for the unit
	// and the generation it was computed for, so the state file cannot be applied to the wrong
	// job after a restart.
	UnitID     string
	Generation int64

	Action Action
	Reason Reason
	// SaveNow is true when Action is ActionSave or ActionRepair.
	SaveNow bool

	// RequestID identifies one request. The job acts on a request at most once, keyed by this field,
	// and the executor settles the write that answers it by the same id. Empty means this decision
	// asks for nothing.
	RequestID string
	// BaselineSeconds is the job's own period while the governor is gone (`no-us`), zero when the
	// user chose none; BaselineNone says which of the two zero means. Both travel so a job that has
	// never met the governor can still behave, and so the saving has a baseline written down.
	BaselineSeconds int64
	BaselineNone    bool
	// WindowSeconds is the user's `with-us`, zero when the window is off. It travels for display and
	// for the job's own bookkeeping; the window itself is the executor's (0016 R1).
	WindowSeconds int64

	// CopiesToWrite is how many copies the next write should produce.
	CopiesToWrite int
	// Kind is what the next write should produce. Empty means full, which is what a producer that
	// cannot write deltas gets and what it did before this field existed.
	Kind WriteKind
	// KeepCopies is how many copies the implementation should keep. It is a floor: even when
	// every copy is older than OldestStepToKeep, this many newest copies stay.
	KeepCopies int
	// OldestStepToKeep is the oldest checkpoint step that should remain. Zero means keep all.
	// The implementation deletes sealed copies older than this step.
	OldestStepToKeep int64

	// Missing lists the measurements that were unknown and changed the decision.
	Missing []string
	// Reasons is the machine-readable cause chain, one entry per step that fired.
	Reasons []ReasonEntry
	// Sensitivity says what would change this answer, so a user can see the threshold rather than only
	// the outcome. It is empty when nothing close would change it.
	Sensitivity []SensitivityEntry
	// Why is one sentence for a human, built from absolute times and measured numbers only.
	Why string
	// Version identifies the policy that produced this decision.
	Version string
}

// Policy is the whole algorithm surface: one function and a name.
//
// The optional axes a policy may use are declared in factor.go, which also says why a factor is
// never allowed to return a Decision.
type Policy interface {
	Name() string
	Decide(Bounds, Snapshot) Decision
}
