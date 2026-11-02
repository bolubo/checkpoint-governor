package verify

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// Every fixture lands in the state its name promises. This is the test the fixture README points at: a
// tree that could mean two states would make this table lie, so the table is written against the files
// rather than against the classifier's own vocabulary.
func TestEachFixtureLandsInItsState(t *testing.T) {
	now := fixedTime()

	cases := []struct {
		fixture string
		want    int64
		opts    Options
		state   policy.CopyState
		reason  Reason
	}{
		{"sealed-ok", 7, Options{Now: now, CheckContent: true, Full: true},
			policy.CopyVerified, ReasonOK},
		{"sealed-ok", 7, Options{Now: now},
			policy.CopySealed, ReasonSealed},
		{"short-shard", 7, Options{Now: now, CheckContent: true, Full: true},
			policy.CopyBroken, ReasonShort},
		{"torn-tail", 7, Options{Now: now, CheckContent: true, Full: true},
			policy.CopyBroken, ReasonHash},
		{"stale-generation", 7, Options{Now: now, CheckContent: true},
			policy.CopyBroken, ReasonStale},
		{"evil-path", 7, Options{Now: now},
			policy.CopyBroken, ReasonInvalid},
		{"unknown-field", 7, Options{Now: now},
			policy.CopyBroken, ReasonInvalid},
		{"sealed-no-tail", 7, Options{Now: now, CheckContent: true},
			policy.CopyBroken, ReasonHash},
		{"absent", 7, Options{Now: now},
			policy.CopyUnverified, ReasonMissing},
	}

	for _, c := range cases {
		dir := filepath.Join("testdata", c.fixture, versionDirOrSelf(t, c.fixture))
		v := Classify(dir, c.want, c.opts)
		if v.State != c.state || v.Reason != c.reason {
			t.Errorf("%s with %+v: got %s/%s (%s), want %s/%s",
				c.fixture, c.opts, v.State, v.Reason, v.Detail, c.state, c.reason)
		}
	}
}

// A directory with no manifest is a write in flight — until the deadline passes, and then it is a writer
// that never sealed. Both halves are tested with an injected clock, because the difference is time and a
// test that sleeps to find it is a test nobody runs.
func TestAManifestlessDirectoryWaitsThenExpires(t *testing.T) {
	dir := filepath.Join("testdata", "no-manifest", "gen-0007")
	deadline := fixedTime().Add(10 * time.Second)

	before := Classify(dir, 7, Options{Now: fixedTime(), Deadline: deadline})
	if before.State != policy.CopyUnverified || before.Reason != ReasonWaiting {
		t.Fatalf("before the deadline: %s/%s, want unverified/waiting", before.State, before.Reason)
	}

	after := Classify(dir, 7, Options{Now: deadline.Add(time.Second), Deadline: deadline})
	if after.State != policy.CopyBroken || after.Reason != ReasonExpired {
		t.Fatalf("after the deadline: %s/%s, want broken/expired", after.State, after.Reason)
	}

	// No deadline announced means no expiry: we do not invent a deadline we were never given.
	noDeadline := Classify(dir, 7, Options{Now: fixedTime().Add(48 * time.Hour)})
	if noDeadline.Reason != ReasonWaiting {
		t.Fatalf("without a deadline: %s, want waiting", noDeadline.Reason)
	}
}

// The cheap check reads the tail, and catches the tear a kill leaves there.
func TestTheTailCheckCatchesATornWrite(t *testing.T) {
	dir := filepath.Join("testdata", "torn-tail", "gen-0007")
	v := Classify(dir, 7, Options{Now: fixedTime(), CheckContent: true})
	if v.Reason != ReasonHash {
		t.Fatalf("tail check: %s/%s (%s), want a hash failure", v.State, v.Reason, v.Detail)
	}
	if v.Detail == "" {
		t.Fatal("a hash failure with no detail names no shard")
	}
}

// A writer that declares no tail hash gets no free pass: without a full read there is nothing to compare,
// and the honest answer is `broken`, not `verified`.
func TestNoDeclaredTailHashIsNotAVerification(t *testing.T) {
	dir := filepath.Join("testdata", "sealed-no-tail", "gen-0007")
	v := Classify(dir, 7, Options{Now: fixedTime(), CheckContent: true})
	if v.State == policy.CopyVerified {
		t.Fatal("a shard with no tail hash was reported verified without reading it")
	}
	if v.Reason != ReasonHash {
		t.Fatalf("reason = %s, want hash", v.Reason)
	}

	// With a full read it can still be verified — the file is intact, only the cheap path is unavailable.
	if full := Classify(dir, 7, Options{Now: fixedTime(), CheckContent: true, Full: true}); full.State != policy.CopyVerified {
		t.Fatalf("full read: %s (%s), want verified", full.State, full.Detail)
	}
}

func versionDirOrSelf(t *testing.T, fixture string) string {
	t.Helper()
	if fixture == "absent" {
		return "gen-9999" // a path that does not exist
	}
	return versionDir(t, fixture)
}
