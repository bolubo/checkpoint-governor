package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A request is small and atomic, or it was never made.
func TestARequestRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "request.json")
	want := Request{Generation: 7, Reason: "risk.preempt", NotAfter: fixedTime(), Full: true}

	if err := WriteRequest(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRequest(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}

	// No temporary file may survive: a stray .request-* is a request nobody can find.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "request.json" {
		t.Fatalf("directory holds %d entries, want only request.json", len(entries))
	}
}

// A truncated request must not be readable. This is the failure a kill leaves behind, and honouring half a
// decision is worse than honouring none.
func TestATruncatedRequestIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "request.json")
	if err := os.WriteFile(path, []byte(`{"generation": 7, "reason": "risk.`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRequest(path); err == nil {
		t.Fatal("a truncated request was accepted")
	}
}

// A request from a newer build carries fields this one does not understand; half-reading it would honour
// half a decision.
func TestARequestWithUnknownFieldsIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "request.json")
	body := `{"generation": 7, "reason": "r", "notAfter": "2026-11-02T03:20:07Z", "full": false, "shards": 4}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRequest(path); err == nil {
		t.Fatal("a request with an unknown field was accepted")
	}
}

// The two things a request must carry to be worth acting on.
func TestARequestNeedsAReasonAndAGeneration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "request.json")

	if err := WriteRequest(path, Request{Generation: 7, NotAfter: time.Now()}); err == nil {
		t.Fatal("a request with no reason was written: that is a cost without a cause")
	}
	if err := WriteRequest(path, Request{Reason: "r", NotAfter: time.Now()}); err == nil {
		t.Fatal("a request with generation 0 was written")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a refused request still produced a file")
	}
}

// A manifest that declares more shards than the cap is refused before anything is allocated for them.
func TestAManifestWithTooManyShardsIsRefusedCheaply(t *testing.T) {
	body := strings.Repeat(`{"name":"a","size":0,"sha256":"`+strings.Repeat("a", 64)+`"},`, MaxShards+1)
	body = `{"generation":1,"step":0,"sealedAt":"2026-11-02T03:20:07Z","writer":"w","shards":[` + body + `]}`
	if _, err := Parse([]byte(body)); err == nil {
		t.Fatal("a manifest past the shard cap was accepted")
	}
}
