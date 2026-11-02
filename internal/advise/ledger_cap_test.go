package advise

import (
	"testing"
	"time"
)

// The ledger is written every round, so anything in it that grows is written every round too.
//
// FirstSeen is keyed by directory: one entry per copy ever written, kept for the life of the job. Thousands of
// entries after a few weeks, in a file that has to stay small enough to rewrite on every round and boring
// enough that nobody has to look at it.
func TestTheLedgerForgetsOldPaths(t *testing.T) {
	dir := t.TempDir()
	l := Ledger{Schema: SchemaVersion, FirstSeen: map[string]int64{}}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Ten times the window: enough that "keep the newest" and "keep the first seen" give different answers.
	const n = firstSeenWindow * 10
	for i := 0; i < n; i++ {
		l.Observe("step-"+itoa10(i), base.Add(time.Duration(i)*time.Second))
	}
	if len(l.FirstSeen) != n {
		t.Fatalf("observed %d paths, ledger holds %d", n, len(l.FirstSeen))
	}

	if err := l.Save(dir); err != nil {
		t.Fatal(err)
	}
	back, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(back.FirstSeen); got > firstSeenWindow {
		t.Fatalf("ledger kept %d entries, want at most %d", got, firstSeenWindow)
	}
	// The newest sighting survived — it is the one a seal can still be matched against.
	newest := "step-" + itoa10(n-1)
	if _, ok := back.FirstSeen[newest]; !ok {
		t.Fatal("the newest sighting was pruned: that is the one still in use")
	}
	// The oldest did not — otherwise nothing was pruned at all.
	if _, ok := back.FirstSeen["step-0"]; ok {
		t.Fatal("the first path ever seen survived, so the cap did not bite")
	}
}

func itoa10(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
