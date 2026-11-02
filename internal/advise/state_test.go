package advise

import (
	"strings"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

func exampleDecision() policy.Decision {
	return policy.Decision{
		UnitID:           "train-1",
		Generation:       7,
		Action:           policy.ActionSave,
		Reason:           policy.ReasonSchedule,
		CopiesToWrite:    2,
		KeepCopies:       2,
		OldestStepToKeep: 12000,
		RequestID:        "req-1",
		BaselineSeconds:  600,
		WindowSeconds:    1800,
		Why:              "newest checkpoint sealed at 2026-01-01T09:50:00Z; the write starts when the age reaches 9m30s",
		Version:          "risk-v1",
	}
}

// The published file is a contract; its bytes are the contract. This test is the one place the
// keys, their order and their spelling are pinned to design.
func TestEncodeMatchesTheDocumentedFile(t *testing.T) {
	got, err := encode(exampleDecision(), 7, time.Unix(1730000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "schema": 2,
  "epoch": 7,
  "expiresAt": 1730000000,
  "unitId": "train-1",
  "generation": 7,
  "action": "save",
  "reason": "schedule",
  "requestId": "req-1",
  "baselineSeconds": 600,
  "baselineNone": false,
  "windowSeconds": 1800,
  "copiesToWrite": 2,
  "kind": "",
  "keepCopies": 2,
  "oldestStepToKeep": 12000,
  "why": "newest checkpoint sealed at 2026-01-01T09:50:00Z; the write starts when the age reaches 9m30s",
  "version": "risk-v1",
  "missing": []
}`
	if string(got) != want {
		t.Fatalf("the file changed shape:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestMissingIsAnEmptyArray(t *testing.T) {
	got, err := encode(policy.Decision{}, 1, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"missing": []`) {
		t.Fatalf("an empty missing list must be [], not null:\n%s", got)
	}
}

// The interval keys are gone: a file that still carried them would tell a reader that a schedule
// exists, which is the one thing the risk core stopped computing (0015 §4).
func TestTheFileCarriesNoIntervalSchedule(t *testing.T) {
	got, err := encode(policy.Decision{}, 1, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"intervalSeconds", "triggerAt", "nextCheckAt", "tightestBoundSeconds", "tax"} {
		if strings.Contains(string(got), `"`+gone+`"`) {
			t.Fatalf("%q is still written:\n%s", gone, got)
		}
	}
}
