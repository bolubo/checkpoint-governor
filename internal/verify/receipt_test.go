package verify

import (
	"strings"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// The one lie a receipt could tell: calling something verified when nobody read the bytes.
func TestAReceiptCannotClaimAVerificationThatDidNotHappen(t *testing.T) {
	v := Verdict{State: policy.CopyVerified, Reason: ReasonOK, Generation: 7, Step: 48210}

	if _, err := NewReceipt(v, Options{Now: fixedTime()}, fixedTime(), fixedTime()); err == nil {
		t.Fatal("a receipt recorded verified with no content check")
	}

	r, err := NewReceipt(v, Options{Now: fixedTime(), CheckContent: true}, fixedTime(), fixedTime())
	if err != nil {
		t.Fatal(err)
	}
	if r.Check != "sample" {
		t.Fatalf("check = %q, want sample", r.Check)
	}
	if full, _ := NewReceipt(v, Options{Now: fixedTime(), CheckContent: true, Full: true}, fixedTime(), fixedTime()); full.Check != "full" {
		t.Fatalf("full check recorded as %q", full.Check)
	}
}

// A sealed copy that was never read stays sealed in the receipt, and says so in the reason.
func TestASealedCopyWithNoReadIsNotVerified(t *testing.T) {
	v := Verdict{State: policy.CopySealed, Reason: ReasonSealed, Generation: 7}
	r, err := NewReceipt(v, Options{Now: fixedTime()}, fixedTime(), fixedTime())
	if err != nil {
		t.Fatal(err)
	}
	if r.VerifiedAt != (time.Time{}) || r.Check != "" {
		t.Fatalf("a sealed copy was recorded as verified: %+v", r)
	}
	if !strings.Contains(r.Line(), string(ReasonSealed)) {
		t.Fatalf("the line does not say why: %q", r.Line())
	}
}

// One line per generation, in the order the three claims are made.
func TestTheReceiptLineSaysWhatHappened(t *testing.T) {
	requested := fixedTime()
	sealed := requested.Add(7 * time.Second)
	verified := sealed.Add(2 * time.Second)

	r := Receipt{
		Generation: 7, Step: 48210, Bytes: 1288490188,
		RequestedAt: requested, SealedAt: sealed, VerifiedAt: verified,
		Check: "sample", Reason: ReasonOK,
	}
	got := r.Line()
	want := "generation 7  requested 03:20:07  sealed 03:20:14  verified 03:20:16  sample  steps 48210  bytes 1.2G"
	if got != want {
		t.Fatalf("line =\n  %q\nwant\n  %q", got, want)
	}

	// A failure line names the shard, because "broken" alone is not actionable.
	fail := Receipt{Generation: 7, Reason: ReasonShort,
		Detail: "shard rank-03.bin is 41 bytes, sealed at 50"}
	line := fail.Line()
	if !strings.Contains(line, "short") || !strings.Contains(line, "rank-03.bin") {
		t.Fatalf("failure line = %q, want the reason and the shard", line)
	}
	if a := fail.Attempt(1, 2); !strings.Contains(a, "attempt 1 of 2") || !strings.Contains(a, "rank-03.bin") {
		t.Fatalf("attempt line = %q", a)
	}
}

// The two numbers the product is judged by.
func TestTheTallySaysWhetherItWasWorthRunning(t *testing.T) {
	half := Tally{Taken: 10, Verified: 8, Invalid: 0}
	if got := half.VerifiedPerTaken(); got != 0.8 {
		t.Fatalf("verified per taken = %v, want 0.8", got)
	}
	if got := half.InvalidRate(); got != 0 {
		t.Fatalf("invalid rate = %v, want 0", got)
	}

	bad := Tally{Taken: 4, Verified: 3, Invalid: 1}
	if got := bad.InvalidRate(); got != 0.25 {
		t.Fatalf("invalid rate = %v, want 0.25", got)
	}

	none := Tally{}
	if none.VerifiedPerTaken() != 0 || none.InvalidRate() != 0 {
		t.Fatal("an empty tally produced a rate, which would read as a real measurement")
	}
}
