package loop

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/notify"
	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/produce"
)

// A plan that changed is reported once, with the identity of the unit and the request the job will
// act on. This is the outward half of the delivery plane: the file is for the job, the event is for
// everybody else.
func TestAChangedPlanIsPosted(t *testing.T) {
	var events []notify.Event
	dir := t.TempDir()
	now := t0
	l, err := Open(Config{
		Dir: dir, UnitID: "job-1", Cluster: "kind-a", Bounds: bounds(), Policy: policy.Risk{},
		Now: func() time.Time { return now }, VerifyEvery: -1,
		Probes: []produce.Probe{&fakeProbe{name: "risk.shutdown", every: time.Second, state: produce.Fresh, value: true}},
		Notify: func(e notify.Event) error {
			events = append(events, e)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	st, err := l.Round()
	if err != nil {
		t.Fatal(err)
	}
	if st.Action != policy.ActionSave || !st.Published {
		t.Fatalf("round = %s published=%v, want a published save", st.Action, st.Published)
	}
	if len(events) != 1 {
		t.Fatalf("posted %d events, want 1: %v", len(events), events)
	}
	e := events[0]
	if e.Unit != "job-1" || e.Cluster != "kind-a" || e.Action != "save" || e.Reason != "risk" {
		t.Fatalf("event = %+v", e)
	}
	if e.RequestID == "" {
		t.Fatal("the event names no request id, so a receiver cannot correlate it with the write")
	}

	// The renewal round must not post: the plan did not change, and a receiver must not be woken for
	// a heartbeat.
	now = now.Add(time.Second)
	if _, err := l.Round(); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("an unchanged plan was posted again: %v", events)
	}
}

// A receiver that refuses must not change what the controller does, and must leave a record. The
// failure is a report about the report, never a condition.
func TestAFailedPostIsRecordedAndDoesNotChangeTheDecision(t *testing.T) {
	dir := t.TempDir()
	now := t0
	l, err := Open(Config{
		Dir: dir, UnitID: "job-1", Bounds: bounds(), Policy: policy.Risk{},
		Now: func() time.Time { return now }, VerifyEvery: -1,
		Probes: []produce.Probe{&fakeProbe{name: "risk.shutdown", every: time.Second, state: produce.Fresh, value: true}},
		Notify: func(e notify.Event) error { return errors.New("receiver said no") },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	st, err := l.Round()
	if err != nil {
		t.Fatalf("a refused notification failed the round: %v", err)
	}
	if st.Action != policy.ActionSave || !st.Published {
		t.Fatalf("round = %s published=%v; a refused post must not change the decision", st.Action, st.Published)
	}

	events, err := advise.ReadEvents(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Code == "notify-failed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no notify-failed event in %s", filepath.Join(dir, ".ckpt-governor", "events.jsonl"))
	}
	if raw, err := os.ReadFile(filepath.Join(dir, ".ckpt-governor", "events.jsonl")); err == nil &&
		!strings.Contains(string(raw), "notify-failed") {
		t.Fatalf("events file does not mention the failure: %s", raw)
	}
}
