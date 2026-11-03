package adapters

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// A reclaim is an arrival the gate stopped: a higher-priority job that cannot fit until some unit
// writes and gives its room back (decision 0030). The gate holds the arrival, writes one tiny record,
// and lets go when the victim's seal lands or the deadline passes.
//
// For the controller this record is an **announcement**, not an inference — the same kind of fact as
// `deletionTimestamp`, written before the platform acts instead of after. It lives beside the unit
// records so the read stays one small file per unit, independent of how many arrivals wait.
const (
	reclaimsDirName = "reclaims"
	reclaimSchema   = 1
)

// The three states a reclaim can be in. The names are the gate's vocabulary and are written to disk,
// so they are part of the world's contract.
const (
	// ReclaimHeld: the arrival is stopped and this unit is the one being asked to write. The platform
	// has decided, but has not acted yet — this is the announcement with lead time.
	ReclaimHeld = "held"
	// ReclaimReleased: the gate let the arrival through, either because the seal landed or because
	// the deadline passed. The announcement is over; a unit that later loses its room hears it from
	// the platform, as it always did.
	ReclaimReleased = "released"
	// ReclaimUncovered: the gate died while holding the arrival and the platform carried on without
	// it. The unit's write was asked for and never promised; the controller must not count it.
	ReclaimUncovered = "uncovered"
)

// ReclaimRecord is `<worldDir>/<cluster>/reclaims/<sha1(unitId)>.json`: one arrival that is waiting
// on one unit.
//
// RequestID is the reclaim's identity, not a checkpoint request. It exists so a gate that retries the
// same arrival does not overwrite a different hold, and so a person can match the line in `explain`
// with the line the gate printed.
type ReclaimRecord struct {
	Schema  int    `json:"schema"`
	Cluster string `json:"cluster,omitempty"`
	UnitID  string `json:"unitId"`

	// RequestID names this reclaim. Intruder names the job the gate stopped, so two holds on one unit
	// are distinguishable in the record and in `explain`.
	RequestID string `json:"requestId"`
	Intruder  string `json:"intruder,omitempty"`
	// NeedMilli is how much room the arrival needs, in milli-cores, as the gate read it. It is not
	// used by any condition: the gate picks the victim, and the controller only needs "am I held".
	NeedMilli int64 `json:"needMilli,omitempty"`

	State string    `json:"state"`
	Since time.Time `json:"since"`
	// Deadline is when the gate stops waiting and releases the arrival with no seal. It is the
	// configurator's number, and the fail-open edge of the design: a hold with no deadline would be a
	// hold forever, so a zero deadline never holds.
	Deadline time.Time `json:"deadline"`
}

// Held says whether this record still stops an arrival at now. Both the state and the deadline have
// to agree: a gate that wrote `held` and then died leaves a record whose deadline eventually passes,
// and the scheduler must resume without it.
func (r ReclaimRecord) Held(now time.Time) bool {
	return r.State == ReclaimHeld && !r.Deadline.IsZero() && now.Before(r.Deadline)
}

// Line is the one sentence `explain` prints for a held record: who is stopped, for which unit, and
// how long the gate will wait. It names the request so the line can be matched with the gate's log.
func (r ReclaimRecord) Line() string {
	intruder := r.Intruder
	if intruder == "" {
		intruder = "a higher-priority job"
	}
	s := "the reclaim gate is holding " + intruder + " for this unit"
	if !r.Deadline.IsZero() {
		s += " until " + r.Deadline.UTC().Format("15:04:05Z")
	}
	if r.RequestID != "" {
		s += " (reclaim " + r.RequestID + ")"
	}
	return s
}

// ReclaimFileName is the on-disk name, the same hash the unit records use: a unit id may contain
// slashes, and a flat directory costs one stat regardless of the number of units.
func ReclaimFileName(unitID string) string { return UnitFileName(unitID) }

// WriteReclaim writes one record atomically. It is the gate's write; the controller never calls it,
// and that is on purpose — a controller that rewrote its own announcement could talk itself into a
// write.
func WriteReclaim(worldDir, cluster string, r ReclaimRecord) error {
	if r.Schema == 0 {
		r.Schema = reclaimSchema
	}
	if r.Cluster == "" {
		r.Cluster = cluster
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(worldDir, cluster, reclaimsDirName, ReclaimFileName(r.UnitID)), raw)
}

// ReadReclaim reads one record. A record whose unit id does not match the one asked for is an error,
// for the same reason a unit record's mismatch is: that is a collision or a renamed unit.
func ReadReclaim(worldDir, cluster, unitID string) (ReclaimRecord, error) {
	var r ReclaimRecord
	raw, err := os.ReadFile(filepath.Join(worldDir, cluster, reclaimsDirName, ReclaimFileName(unitID)))
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, fmt.Errorf("reclaim record: %w", err)
	}
	if r.UnitID != unitID {
		return r, fmt.Errorf("reclaim record says %q, asked for %q", r.UnitID, unitID)
	}
	return r, nil
}

// ReadHeldReclaim answers the only question the condition layer asks: is an arrival stopped on this
// unit right now. A missing record is "no", not an error — gate deployments and plain deployments
// share the same world, and the file is how they differ.
func ReadHeldReclaim(worldDir, cluster, unitID string, now time.Time) (ReclaimRecord, bool, error) {
	r, err := ReadReclaim(worldDir, cluster, unitID)
	if err != nil {
		if os.IsNotExist(err) {
			return ReclaimRecord{}, false, nil
		}
		return ReclaimRecord{}, false, err
	}
	if !r.Held(now) {
		return r, false, nil
	}
	return r, true, nil
}
