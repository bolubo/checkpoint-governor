package adapters

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// The world is what the implementation side knows about the cluster. The observer writes it; the
// controller reads a summary and one tiny file per unit. Splitting the two is what keeps the read
// cost independent of how many units or clusters exist: a summary is node-sized, a unit record is a
// few hundred bytes, and neither grows with the job count.
const (
	worldSchema       = 1
	summaryName       = "summary.json"
	unitsDirName      = "units"
	worldStaleDefault = 30 * time.Second
)

// Resources is one row of the accounting, already normalised. Kubernetes hands these out as strings
// with suffixes ("24", "16373440Ki"); the observer converts once, because a conversion done by every
// consumer is a conversion done differently by every consumer.
type Resources struct {
	CPUMilli int64 `json:"cpuMilli"`
	MemBytes int64 `json:"memBytes"`
	GPUCount int64 `json:"gpuCount"`
}

// NodeState is one node as the world sees it.
type NodeState struct {
	Name        string    `json:"name"`
	Allocatable Resources `json:"allocatable"`
	Requested   Resources `json:"requested"`
	Cordoned    bool      `json:"cordoned"`
	Taints      []string  `json:"taints,omitempty"`
	// RequestsUnknown says no bound pod on this node declares requests. The sum is then zero for a
	// reason that has nothing to do with being idle, and a ratio computed from it would call a full
	// node empty. Only the observer can tell the difference, so it says so.
	RequestsUnknown bool `json:"requestsUnknown,omitempty"`
	// PendingHigherPriority is how many pending pods outrank the highest-priority unit already on
	// this node. It answers "who would win", which is the question an eviction risk asks; "is
	// anything queued" is a different and much weaker question.
	PendingHigherPriority int `json:"pendingHigherPriority"`
}

// WorldSummary is `summary.json`: one per cluster, node-sized.
type WorldSummary struct {
	Schema            int                       `json:"schema"`
	Cluster           string                    `json:"cluster"`
	At                time.Time                 `json:"at"`
	StaleAfterSeconds int                       `json:"staleAfterSeconds"`
	Capabilities      map[string]CapabilityWire `json:"capabilities,omitempty"`
	// Queues is what each queue can still admit and whether it may preempt: the two facts the measured
	// rule needs besides "somebody higher is waiting" (0022).
	Queues []QueueWire `json:"queues,omitempty"`
	// PendingEvent names the fact "higher-priority work is waiting for room", as one id for the whole
	// cluster (0022). It is a fact about the arrival — not about any unit, and not about what the
	// scheduler will do with it. While the same work waits the id stays the same, so one write answers
	// it; a different arrival is a different event with its own decision.
	PendingEvent string      `json:"pendingEvent,omitempty"`
	Nodes        []NodeState `json:"nodes"`
}

// CapabilityWire is the on-disk shape of a capability inside a summary, so a summary and
// deployment.json can be read by the same eye.
type CapabilityWire struct {
	Exists bool   `json:"exists"`
	Source string `json:"source,omitempty"`
}

// UnitRecord is `units/<sha1(unitId)>.json`: one tiny file per tracked unit.
type UnitRecord struct {
	Schema            int        `json:"schema"`
	Cluster           string     `json:"cluster"`
	UnitID            string     `json:"unitId"`
	At                time.Time  `json:"at"`
	Node              string     `json:"node"`
	Priority          int32      `json:"priority"`
	QoS               string     `json:"qos"`
	DeletionTimestamp *time.Time `json:"deletionTimestamp"`
	Evicted           bool       `json:"evicted"`
	// HigherPriorityWaiting counts the held-back jobs that outrank this unit: the second term of the
	// measured rule, and zero means this unit cannot be the one taken to make room.
	HigherPriorityWaiting int `json:"higherPriorityWaiting,omitempty"`
	// Permit is the fleet's verdict for this unit (docs/decisions/0021). Absent means permission: an
	// observer that predates the field, a cluster with one unit, and every unreadable value all leave
	// the governor behaving exactly as it did before.
	Permit *UnitPermit `json:"permit,omitempty"`
}

// UnitPermit is the observer's answer to a question one unit cannot answer: when a cause endangers
// several units, may this one spend a write?
type UnitPermit struct {
	Verdict string `json:"verdict"`
	// Cause is the kind of pressure: reclaim, drain, serialized.
	Cause string     `json:"cause,omitempty"`
	Rank  int        `json:"rank,omitempty"`
	Until *time.Time `json:"until,omitempty"`
}

// Age is how old the reading is. It is shown next to every number the world contributes.
func (s WorldSummary) Age(now time.Time) time.Duration {
	if s.At.IsZero() {
		return 0
	}
	return now.Sub(s.At)
}

// Stale says whether the summary may still be believed. A frozen file is not calm: it is the same as
// no file, except that it looks alive.
func (s WorldSummary) Stale(now time.Time) bool {
	limit := time.Duration(s.StaleAfterSeconds) * time.Second
	if limit <= 0 {
		limit = worldStaleDefault
	}
	return s.Age(now) > limit
}

// Node finds one node by name.
func (s WorldSummary) Node(name string) (NodeState, bool) {
	for _, n := range s.Nodes {
		if n.Name == name {
			return n, true
		}
	}
	return NodeState{}, false
}

// ReadSummary reads `<worldDir>/<cluster>/summary.json`.
func ReadSummary(worldDir, cluster string) (WorldSummary, error) {
	var s WorldSummary
	raw, err := os.ReadFile(filepath.Join(worldDir, cluster, summaryName))
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("%s: %w", summaryName, err)
	}
	if s.Schema != 0 && s.Schema != worldSchema {
		return s, fmt.Errorf("%s: unsupported schema %d", summaryName, s.Schema)
	}
	return s, nil
}

// WriteSummary writes a summary atomically. It is used by the fake observer in tests and by
// `ckptctl world --fake`; the real observer lives in contrib/observer and writes the same shape.
func WriteSummary(worldDir string, s WorldSummary) error {
	if s.Schema == 0 {
		s.Schema = worldSchema
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(worldDir, s.Cluster, summaryName), raw)
}

// UnitFileName is the on-disk name for a unit record: the hash keeps the directory flat and avoids
// encoding a unit id that may contain slashes.
func UnitFileName(unitID string) string {
	sum := sha1.Sum([]byte(unitID))
	return hex.EncodeToString(sum[:]) + ".json"
}

// WriteUnit writes a unit record atomically.
func WriteUnit(worldDir, cluster string, u UnitRecord) error {
	if u.Schema == 0 {
		u.Schema = worldSchema
	}
	if u.Cluster == "" {
		u.Cluster = cluster
	}
	raw, err := json.MarshalIndent(u, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(worldDir, cluster, unitsDirName, UnitFileName(u.UnitID)), raw)
}

// ReadUnit reads one unit record. A record whose id does not match the one asked for is an error,
// not a match: that is a hash collision or a renamed unit, and both deserve to be seen.
func ReadUnit(worldDir, cluster, unitID string) (UnitRecord, error) {
	var u UnitRecord
	raw, err := os.ReadFile(filepath.Join(worldDir, cluster, unitsDirName, UnitFileName(unitID)))
	if err != nil {
		return u, err
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return u, fmt.Errorf("unit record: %w", err)
	}
	if u.UnitID != unitID {
		return u, fmt.Errorf("unit record says %q, asked for %q", u.UnitID, unitID)
	}
	return u, nil
}

// UnitTracking says whether this cluster's observer writes unit records at all. It is what
// distinguishes "this deployment does not track units" (absent) from "it tracks them and lost
// mine" (unknown) — a difference that decides between silence and a write.
func UnitTracking(worldDir, cluster string) bool {
	entries, err := os.ReadDir(filepath.Join(worldDir, cluster, unitsDirName))
	return err == nil && len(entries) > 0
}

// Clusters lists the clusters that have a summary, sorted. It is what the display header counts.
func Clusters(worldDir string) []string {
	entries, err := os.ReadDir(worldDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(worldDir, e.Name(), summaryName)); err == nil {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// writeAtomic writes through a temporary file in the same directory and renames it, so a reader
// never sees half a snapshot.
func writeAtomic(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// QueueWire is one queue's numbers, as the observer published them.
type QueueWire struct {
	Name        string `json:"name"`
	FreeMilli   int64  `json:"freeMilli"`
	DemandMilli int64  `json:"demandMilli"`
	Allowed     bool   `json:"allowed"`
}

// ShortfallMilli is how much more the waiting work needs than the queue can give.
func (q QueueWire) ShortfallMilli() int64 {
	if d := q.DemandMilli - q.FreeMilli; d > 0 {
		return d
	}
	return 0
}

// PressingQueue picks the queue that is actually under pressure: the one with the largest shortfall.
//
// With one queue — the common deployment — this is exact. With several it is an upper bound on the
// shortfall a unit in any of them faces, and the display names the queue it read so a person can see
// which one that was.
func (s WorldSummary) PressingQueue() (QueueWire, bool) {
	if len(s.Queues) == 0 {
		return QueueWire{}, false
	}
	best := s.Queues[0]
	for _, q := range s.Queues[1:] {
		if q.ShortfallMilli() > best.ShortfallMilli() {
			best = q
		}
	}
	return best, true
}
