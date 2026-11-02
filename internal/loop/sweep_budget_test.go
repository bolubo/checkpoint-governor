package loop

import "testing"

// The invariant, stated without any files: a round must be able to give back what it took in.
//
// Without it a fixed budget loses ground every round. With 10 GiB copies and a 256 MiB budget that is forty
// rounds of falling behind per write — which is how a volume fills up while every check reports success.
func TestTheSweepBudgetCoversWhatWasRetired(t *testing.T) {
	cases := []int64{0, 1, sweepBytesPerRound - 1, sweepBytesPerRound, sweepBytesPerRound + 1, 10 << 30}
	for _, retired := range cases {
		if got := sweepBudgetFor(retired); got < retired {
			t.Fatalf("sweepBudgetFor(%d) = %d, which cannot reclaim what the round retired", retired, got)
		}
	}
	if got := sweepBudgetFor(0); got != sweepBytesPerRound {
		t.Fatalf("an idle round got %d, want the base budget %d", got, sweepBytesPerRound)
	}
}
