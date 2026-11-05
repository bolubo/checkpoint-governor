package loop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/exec"
	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/produce"
)

// The receipt is the artifact a person keeps, so it has to say where the copy lives: the same disk as
// the job. A copy outlives the process, not the volume — a fact that must not be left to inference,
// and must appear whether or not the run wrote anything.
func TestReceiptStatesDurability(t *testing.T) {
	cases := []struct {
		name string
		r    Receipt
	}{
		{"nothing written", Receipt{}},
		{"writes", Receipt{Asked: 2, Sealed: 2, Kinds: map[string]int{"window": 2}, Window: 30 * time.Minute}},
	}
	const want = "durability      local disk: a copy outlives the process, not the volume"
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines := strings.Join(c.r.Lines(), "\n")
			if !strings.Contains(lines, want) {
				t.Fatalf("the receipt does not state durability:\n%s", lines)
			}
		})
	}
}

// TestTheOperatorSeesTheTruthInOnePlace is S1's acceptance: one line that answers "what happened",
// "what cannot be seen" and "why the last write did not start", read from the loop's own state.
func TestTheOperatorSeesTheTruthInOnePlace(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := os.MkdirAll(filepath.Join(dir, advise.BaseDir), 0o755); err != nil {
		t.Fatal(err)
	}
	glue := filepath.Join(dir, "glue")
	if err := os.WriteFile(glue, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A history to render: one write that sealed five seconds after it started and ended half a
	// minute ago, and one request that produced nothing.
	sealed := exec.NewAttempt("u", 1, 10, exec.TriggerCondition, "risk.preempt", now.Add(-35*time.Second), now.Add(-30*time.Second))
	sealed.Outcome, sealed.EndedAt, sealed.Bytes = exec.Sealed, now.Add(-30*time.Second), 100
	failed := exec.NewAttempt("u", 1, 20, exec.TriggerCondition, "risk.preempt", now.Add(-time.Hour), now.Add(-50*time.Minute))
	failed.Outcome, failed.EndedAt = exec.Failed, now.Add(-50*time.Minute)
	if err := (exec.Ledger{Schema: exec.SchemaVersion, Attempts: []exec.Attempt{sealed, failed}}).Save(dir); err != nil {
		t.Fatal(err)
	}

	l, err := Open(Config{
		Dir: dir, UnitID: "u",
		Bounds: policy.Bounds{NoUs: policy.NoUsNone, MinCopies: 1},
		Policy: policy.Risk{}, Now: func() time.Time { return now },
		Probes: []produce.Probe{glueProbe{glue}}, BlindAfter: 3,
		VerifyEvery: -1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Round 1 is readable and calm. Then the glue file disappears: two rounds of hiccup, the third
	// without eyes. A blind required condition is urgent, and the cooldown refuses the write because
	// the seal is half a minute old — so the last thing the gate said is `cooldown`, and the refusal
	// is in the history.
	if _, err := l.Round(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(glue); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		now = now.Add(2 * time.Second)
		if _, err := l.Round(); err != nil {
			t.Fatal(err)
		}
	}

	r := l.Report()
	fmt.Printf("%s\n", r)
	if r.Total() != 3 || r.Attempts.Sealed != 1 || r.Attempts.Refused != 1 {
		t.Fatalf("counts = %s, want three attempts with one sealed and one refused", r.Attempts)
	}
	if len(r.Blind) != 1 || r.Blind[0] != "risk.preempt" {
		t.Fatalf("blind = %v, want risk.preempt", r.Blind)
	}
	if r.Gate != "cooldown" {
		t.Fatalf("last gate = %q, want cooldown", r.Gate)
	}
	if r.Last == nil || r.Last.Outcome != exec.Refused {
		t.Fatalf("last attempt = %+v, want the refused one", r.Last)
	}
	if _, ok := r.ByTrigger[exec.TriggerCondition]; !ok {
		t.Fatalf("by trigger = %v, want the condition trigger", r.ByTrigger)
	}

	// The same facts reach the file the view reads, and the view prints them: the report is derived,
	// but the status file must carry it or a person without a debugger sees nothing.
	led := l.Ledger().Status
	if len(led.Attempts) != 3 || led.Blind != 1 || led.LastGate != "cooldown" {
		t.Fatalf("status file: %d attempts, blind %d, last gate %q", len(led.Attempts), led.Blind, led.LastGate)
	}
	var view strings.Builder
	advise.Render(&view, led, now, 10*time.Minute)
	for _, line := range strings.Split(view.String(), "\n") {
		if strings.HasPrefix(line, "attempts ") || strings.HasPrefix(line, "watch ") {
			fmt.Printf("view: %s\n", line)
		}
	}
	if !strings.Contains(view.String(), "last gate cooldown") {
		t.Fatalf("the view does not print the last gate:\n%s", view.String())
	}
}
