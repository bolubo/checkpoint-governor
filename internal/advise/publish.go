package advise

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// Publish writes the decision into <dir>/.ckpt-governor/state.json and reports whether the file
// changed.
//
// Two rules decide "changed":
//
//   - if the plan is the same and the file still has more than half its life left, write nothing. A
//     rewrite touches the mtime, a job watching the file re-reads a plan that did not change, and an
//     operator watching mtimes sees activity that never happened;
//   - if the plan is the same but the file is near expiry, write it again with a new stamp. That is
//     the renewal, and it has to happen or a working controller would let its own file expire.
//
// "Near expiry" is measured against the clock, not against whole seconds: with a three-round TTL,
// comparing truncated seconds leaves a window in which a live controller is indistinguishable from a
// dead one.
func Publish(dir string, d policy.Decision, epoch int64, now time.Time, ttl time.Duration) (bool, error) {
	expiry := ttl
	if expiry <= 0 {
		expiry = DefaultPlanTTL
	}
	next := stateOf(d, epoch, now.Add(expiry))
	path := filepath.Join(dir, BaseDir, stateName)

	if old, ok := readState(path); ok && sameButExpiry(old, next) {
		if time.Unix(old.ExpiresAt, 0).Sub(now) >= expiry/2 {
			return false, nil
		}
	}

	raw, err := encode(d, epoch, now.Add(expiry))
	if err != nil {
		return false, err
	}
	if err := writeAtomic(path, raw); err != nil {
		return false, err
	}
	return true, nil
}

// Renew re-stamps the plan already on disk without changing the decision it carries, and reports
// whether it wrote. It is the heartbeat, and it is deliberately independent of the decision's own
// throttle: "measure often, decide rarely" is a rule about the policy, while the plan's expiry is a
// rule about being alive. A plan that expires between two decisions tells a healthy job that we are
// gone — and with `no-us none` a job that believes that writes nothing at all.
func Renew(dir string, now time.Time, ttl time.Duration) (bool, error) {
	path := filepath.Join(dir, BaseDir, stateName)
	s, ok := readState(path)
	if !ok {
		return false, nil // nothing to renew: the next decision publishes a plan
	}
	expiry := ttl
	if expiry <= 0 {
		expiry = DefaultPlanTTL
	}
	if time.Unix(s.ExpiresAt, 0).Sub(now) >= expiry/2 {
		return false, nil
	}
	s.ExpiresAt = now.Add(expiry).Unix()
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return false, err
	}
	if err := writeAtomic(path, raw); err != nil {
		return false, err
	}
	return true, nil
}

// SameAsPublished reports whether the file already holds this plan, ignoring the expiry stamp.
//
// The loop uses it to tell a **renewal** (the same plan, a new stamp) from a **change** (a decision
// that flapped), and the two need different limits: a renewal is how a live controller stays visible
// to the job — without it the file expires and a working governor looks dead — while a change is the
// thing that overwrites a file the job may be reading right now.
func SameAsPublished(dir string, d policy.Decision) bool {
	s, ok := readState(filepath.Join(dir, BaseDir, stateName))
	if !ok {
		return false
	}
	return sameButExpiry(s, stateOf(d, s.Epoch, time.Unix(s.ExpiresAt, 0)))
}

// DefaultPlanTTL is how long a published plan stays valid when the caller does not say. It is a few
// rounds, not a few minutes: the plan means "a live governor exists", so a dead one must be noticed
// within a handful of rounds — not within two `no-us` periods, which is what the old arithmetic
// published by accident (design-v1 §5).
const DefaultPlanTTL = 15 * time.Second

// readState reads the published file, reporting false when it is absent or unreadable. An
// unreadable file is treated as absent: the next publish overwrites it, which is what a corrupted
// plan deserves.
func readState(path string) (stateFile, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return stateFile{}, false
	}
	var s stateFile
	if err := json.Unmarshal(raw, &s); err != nil {
		return stateFile{}, false
	}
	// A file from a governor that speaks a different contract is not a plan. Schema 1's expiresAt was
	// a period rather than a liveness stamp (see SchemaVersion), so reading it here would answer a
	// different question than the file asked — and Renew would re-stamp it into a file that is
	// neither version.
	if s.Schema < MinSchema || s.Schema > MaxSchema {
		return stateFile{}, false
	}
	return s, true
}

// Reads is the human form of the range: "2..2" today, "2..3" when a build can read a newer plan.
func Reads() string {
	if MinSchema == MaxSchema {
		return strconv.Itoa(MinSchema)
	}
	return strconv.Itoa(MinSchema) + ".." + strconv.Itoa(MaxSchema)
}

// PlanSchema returns the schema number in the published plan, and false when there is no plan or the
// file is not JSON. It reads only that field: a plan from another version may not decode into
// today's structure, and the number is the part a reader can trust.
func PlanSchema(dir string) (int, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, BaseDir, stateName))
	if err != nil {
		return 0, false
	}
	var head struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return 0, false
	}
	return head.Schema, true
}

// SchemaProblem explains why the plan file cannot be used: it was written by a governor that speaks a
// different contract. Empty when the file is absent or readable.
//
// This is separate from readState because a refusal has to be visible. "This file is from another
// version" and "there is no file" lead to the same safe behaviour — the job falls back to its own
// timer — but only one of them is something a person can act on.
func SchemaProblem(dir string) string {
	schema, ok := PlanSchema(dir)
	if !ok {
		if raw, err := os.ReadFile(filepath.Join(dir, BaseDir, stateName)); err == nil && len(raw) > 0 {
			return "the plan file is not valid JSON"
		}
		return ""
	}
	if schema >= MinSchema && schema <= MaxSchema {
		return ""
	}
	return fmt.Sprintf("the plan file says schema %d; this build reads %s and writes %d",
		schema, Reads(), SchemaVersion)
}

// sameButExpiry compares two plans ignoring the stamp, which is the field a renewal changes.
func sameButExpiry(a, b stateFile) bool {
	a.ExpiresAt = b.ExpiresAt
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// writeAtomic writes through a temporary name in the same directory.
//
// A reader opens state.json at arbitrary moments; a partial file would be a partial plan. The
// directory fsync is what makes the rename survive a power cut, and it is why the temporary file is
// created next to the target rather than in the system's temp directory.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".write-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename

	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
