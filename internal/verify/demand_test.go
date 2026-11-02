package verify

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// One table, one action per row. Every decision has to be explainable in one line, and this table is where
// "why did it ask again" is answered before it is answered in production.
func TestWhatToDoAboutEachVerdict(t *testing.T) {
	now := fixedTime()
	open := Budget{Attempts: 3, Window: time.Minute}

	cases := []struct {
		name string
		v    Verdict
		want int64
		p    Progress
		act  Action
		gen  int64
	}{
		{"verified", Verdict{State: policy.CopyVerified, Reason: ReasonOK, Generation: 7}, 7,
			Progress{}, ActionDone, 7},
		{"sealed owes a read", Verdict{State: policy.CopySealed, Reason: ReasonSealed, Generation: 7}, 7,
			Progress{}, ActionVerify, 7},
		{"in flight", Verdict{State: policy.CopyUnverified, Reason: ReasonWaiting}, 7,
			Progress{}, ActionWait, 0},
		{"older than asked", Verdict{State: policy.CopyBroken, Reason: ReasonStale, Generation: 6}, 7,
			Progress{}, ActionWait, 7},
		{"short", Verdict{State: policy.CopyBroken, Reason: ReasonShort, Generation: 7}, 7,
			Progress{}, ActionRewrite, 8},
		{"hash", Verdict{State: policy.CopyBroken, Reason: ReasonHash, Generation: 7}, 7,
			Progress{}, ActionRewrite, 8},
		{"expired", Verdict{State: policy.CopyBroken, Reason: ReasonExpired}, 7,
			Progress{}, ActionRewrite, 8},
		{"missing", Verdict{State: policy.CopyUnverified, Reason: ReasonMissing}, 7,
			Progress{}, ActionRewrite, 8},
		{"invalid", Verdict{State: policy.CopyBroken, Reason: ReasonInvalid}, 7,
			Progress{}, ActionRewrite, 8},
		{"cannot fit", Verdict{State: policy.CopyBroken, Reason: ReasonShort, Generation: 7}, 7,
			Progress{CannotFit: true}, ActionGiveUp, 7},
		{"attempts spent", Verdict{State: policy.CopyBroken, Reason: ReasonHash, Generation: 7}, 7,
			Progress{Attempts: 3, FirstAt: now}, ActionGiveUp, 7},
		{"window spent", Verdict{State: policy.CopyBroken, Reason: ReasonShort, Generation: 7}, 7,
			Progress{Attempts: 1, FirstAt: now.Add(-2 * time.Minute)}, ActionGiveUp, 7},
		// A sealed generation ahead of the request still moves the number forward, never back.
		{"ahead of the request", Verdict{State: policy.CopyBroken, Reason: ReasonHash, Generation: 9}, 7,
			Progress{}, ActionRewrite, 10},
	}

	for _, c := range cases {
		d := Next(c.v, c.want, c.p, open, now)
		if d.Action != c.act {
			t.Errorf("%s: action = %s (%s), want %s", c.name, d.Action, d.Detail, c.act)
		}
		if c.act == ActionRewrite && d.Generation != c.gen {
			t.Errorf("%s: generation = %d, want %d", c.name, d.Generation, c.gen)
		}
		if d.Detail == "" {
			t.Errorf("%s: a decision with no detail cannot be explained later", c.name)
		}
	}
}

// The property the whole numbering exists for: a write that finishes late cannot answer a newer request.
func TestALateWriteCannotAnswerANewerRequest(t *testing.T) {
	dir := filepath.Join("testdata", "sealed-ok", "gen-0007")

	// Generation 7 is on disk and verified against a request for 7.
	if v := Classify(dir, 7, Options{Now: fixedTime(), CheckContent: true}); v.State != policy.CopyVerified {
		t.Fatalf("generation 7 against a request for 7: %s (%s)", v.State, v.Detail)
	}

	// After a rewrite was asked for, the same tree is stale — it is not an answer, however whole it is.
	v := Classify(dir, 8, Options{Now: fixedTime(), CheckContent: true})
	if v.Reason != ReasonStale {
		t.Fatalf("generation 7 against a request for 8: %s (%s), want stale", v.Reason, v.Detail)
	}
	if d := Next(v, 8, Progress{}, Budget{Attempts: 3, Window: time.Minute}, fixedTime()); d.Action != ActionWait {
		t.Fatalf("a stale copy produced %s; rewriting would ask for a third generation", d.Action)
	}
}

// A rewrite says what is wrong in terms the writer can act on: "retry" is not a diagnosis.
func TestARewriteSaysWhy(t *testing.T) {
	d := Next(Verdict{State: policy.CopyBroken, Reason: ReasonShort, Generation: 7,
		Detail: "shard rank-03.bin is 41 bytes, sealed at 50"},
		7, Progress{}, Budget{Attempts: 3}, fixedTime())

	if d.Action != ActionRewrite {
		t.Fatalf("action = %s", d.Action)
	}
	if d.Detail == "" || d.Detail == "retry" {
		t.Fatalf("detail = %q, which a writer cannot act on", d.Detail)
	}
}
