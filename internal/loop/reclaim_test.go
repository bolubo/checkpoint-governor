package loop

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/exec"
	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// A held reclaim is a clock we do not own: the gate is waiting on this unit's seal before it lets an
// arrival into the cluster. The throttle and the cooldown are both about not writing twice for the
// same situation, and a reclaim is a new situation (0030). Without this, a victim that wrote five
// seconds ago waits out the 60s cooldown while the gate's deadline passes.
func TestAHeldReclaimBypassesTheThrottleAndTheCooldown(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name    string
		reclaim bool
		decided bool
		running int
	}{
		{"no reclaim", false, false, 0},
		{"reclaim", true, true, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSealed(t, dir, 10, 100)
			if err := os.MkdirAll(filepath.Join(dir, advise.BaseDir), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, advise.BaseDir, "step"), []byte("20"), 0o644); err != nil {
				t.Fatal(err)
			}
			// The last write ended five seconds ago: inside the 60s cooldown floor.
			prev := exec.NewAttempt("u", 1, 10, exec.TriggerCondition, "risk.preempt",
				now.Add(-10*time.Second), now.Add(-5*time.Second))
			prev.Outcome, prev.EndedAt, prev.Bytes = exec.Sealed, now.Add(-5*time.Second), 100
			if err := (exec.Ledger{Schema: exec.SchemaVersion, Attempts: []exec.Attempt{prev}}).Save(dir); err != nil {
				t.Fatal(err)
			}

			cfg := Config{
				Dir: dir, UnitID: "u", Holder: "",
				Bounds: policy.Bounds{NoUs: policy.NoUsNone, MinCopies: 1},
				Policy: savesNow{}, Now: func() time.Time { return now },
				VerifyEvery: -1,
			}
			if c.reclaim {
				cfg.Reclaim = func(time.Time) (string, bool) { return "reclaim:r-1", true }
			}
			l, err := Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			// The throttle would wait a minute: the seal is already recorded, so nothing else can
			// make the loop decide.
			l.ledger.HasSeal, l.ledger.LastSealStep, l.ledger.DecidedSealStep = true, 10, 10
			l.ledger.NextDecisionAt = now.Add(time.Minute).UnixNano()

			st, err := l.Round()
			if err != nil {
				t.Fatal(err)
			}
			if st.Decided != c.decided {
				t.Fatalf("decided = %v, want %v", st.Decided, c.decided)
			}
			if st.Attempts.Running != c.running {
				t.Fatalf("running attempts = %d, want %d (gate %q)", st.Attempts.Running, c.running, st.Gate)
			}
		})
	}
}

// The supervisor asks NeedsAttention for every unit every tick. An unpaid reclaim has to be able to
// wake that unit; once the write is recorded, the ordinary throttle applies again.
func TestAnUnpaidReclaimWakesTheUnit(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l := &Loop{
		cfg: Config{
			Reclaim: func(time.Time) (string, bool) { return "reclaim:r-1", true },
			Now:     func() time.Time { return now },
		},
		window: exec.NewWindow(0),
	}
	l.ledger.NextDecisionAt = now.Add(time.Minute).UnixNano()
	if !l.NeedsAttention(now) {
		t.Fatal("an unpaid reclaim did not wake the unit")
	}
	l.ledger.CauseWrittenID = "reclaim:r-1"
	if l.NeedsAttention(now) {
		t.Fatal("a paid reclaim woke the unit past the throttle")
	}
}
