package loop

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/policy"
)

func writeRankStep(t *testing.T, dir string, rank int, step string) {
	t.Helper()
	path := filepath.Join(dir, ".ckpt-governor", "ranks", strconv.Itoa(rank))
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if step == "" {
		return // a rank that exists and has not reported: silent, not absent
	}
	if err := os.WriteFile(filepath.Join(path, "step"), []byte(step), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The controller reads each rank's own step: the slowest one is the unit's progress, the spread is
// printed, and a silent rank is named — never a reason to skip a write.
func TestMembershipReachesTheStatus(t *testing.T) {
	dir := t.TempDir()
	writeRankStep(t, dir, 0, "100")
	writeRankStep(t, dir, 1, "96")
	l, err := Open(Config{Dir: dir, UnitID: "u", Bounds: policy.Bounds{NoUs: policy.NoUsNone, MinCopies: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	l.glue = readGlue(dir)
	l.noteMembership()
	st := l.Ledger().Status
	if st.MembersTotal != 2 || st.MembersReporting != 2 || st.MembersSpread != 4 {
		t.Fatalf("status = %+v, want 2/2 spread 4", st)
	}
	got := stepOfGlue(l.glue, filepath.Join(dir, ".ckpt-governor", "step"))
	if got == nil || *got != 96 {
		t.Fatalf("progress = %v, want the slowest rank 96", got)
	}

	var out bytes.Buffer
	advise.Render(&out, st, time.Now(), 0)
	if !bytes.Contains(out.Bytes(), []byte("members         2/2 · spread 4")) {
		t.Fatalf("status does not print the membership:\n%s", out.String())
	}

	// A rank that exists but never reported is named, and the others still write.
	writeRankStep(t, dir, 2, "")
	l.glue = readGlue(dir)
	l.noteMembership()
	var silent bytes.Buffer
	advise.Render(&silent, l.Ledger().Status, time.Now(), 0)
	if !bytes.Contains(silent.Bytes(), []byte("member 2 silent")) {
		t.Fatalf("a silent rank was not named:\n%s", silent.String())
	}
	if l.Ledger().Status.MembersReporting != 2 {
		t.Fatalf("reporting = %d, want 2", l.Ledger().Status.MembersReporting)
	}
}

// A single writer has no `ranks/`: no members line, and the status is exactly what it was.
func TestASingleWriterStatusHasNoMembersLine(t *testing.T) {
	st := advise.Status{UnitID: "u"}
	var out bytes.Buffer
	advise.Render(&out, st, time.Now(), 0)
	if bytes.Contains(out.Bytes(), []byte("members")) {
		t.Fatalf("a single writer printed a members line:\n%s", out.String())
	}
}
