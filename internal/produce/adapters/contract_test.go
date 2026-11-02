package adapters

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/produce"
)

// The probe contract: for every condition, the three answers must be reachable and distinguishable.
// A condition that can only say 0 is a condition that cannot report the thing it exists for.
//
// The table is the test: each row names a probe, the environment that produces one answer, and the
// answer it must give. `must` then asserts the rows actually cover what each probe claims.
type row struct {
	probe string
	env   produce.Env
	want  produce.State
	value bool
	make  func(t *testing.T) produce.Probe
}

func TestTheProbeContract(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ckpt-governor"), 0o755); err != nil {
		t.Fatal(err)
	}
	endingFile := filepath.Join(dir, ".ckpt-governor", "ending")
	stoppingFile := filepath.Join(dir, ".ckpt-governor", "stopping")
	big := produce.Env{Now: t0, Dir: dir, CheckpointBytes: 1 * gb}

	statFree := func(free int64) func(string) (int64, int64, error) {
		return func(string) (int64, int64, error) { return free, 100 * gb, nil }
	}
	statErr := func(string) (int64, int64, error) { return 0, 0, errors.New("statfs: denied") }

	rows := []row{
		// store.low-space: fires below 2× the checkpoint, calm above, unknown when statfs fails.
		{"store.low-space", big, produce.Fresh, true, func(*testing.T) produce.Probe {
			return lowSpace{every: time.Second, factor: 2, stat: statFree(1 * gb)}
		}},
		{"store.low-space", big, produce.Fresh, false, func(*testing.T) produce.Probe {
			return lowSpace{every: time.Second, factor: 2, stat: statFree(10 * gb)}
		}},
		{"store.low-space", big, produce.Unknown, false, func(*testing.T) produce.Probe {
			return lowSpace{every: time.Second, factor: 2, stat: statErr}
		}},

		// store.blocked: a diagnostic that can say yes and no.
		{"store.blocked", big, produce.Fresh, true, func(*testing.T) produce.Probe {
			return blocked{every: time.Second, probe: func(string) error { return errors.New("read-only") }}
		}},
		{"store.blocked", big, produce.Fresh, false, func(*testing.T) produce.Probe {
			return blocked{every: time.Second, probe: func(string) error { return nil }}
		}},

		// job.ending: the file's presence is the signal.
		{"job.ending", big, produce.Fresh, true, func(t *testing.T) produce.Probe {
			if err := os.WriteFile(endingFile, []byte("1"), 0o644); err != nil {
				t.Fatal(err)
			}
			return presence{condition: "job.ending", file: "ending", every: time.Second}
		}},
		{"job.ending", big, produce.Fresh, false, func(t *testing.T) produce.Probe {
			if err := os.Remove(endingFile); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			return presence{condition: "job.ending", file: "ending", every: time.Second}
		}},

		// risk.shutdown: a different file from job.ending, and a different meaning.
		{"risk.shutdown", big, produce.Fresh, true, func(t *testing.T) produce.Probe {
			if err := os.WriteFile(stoppingFile, []byte("1"), 0o644); err != nil {
				t.Fatal(err)
			}
			return presence{condition: "risk.shutdown", file: "stopping", every: time.Second}
		}},
		{"risk.shutdown", big, produce.Fresh, false, func(t *testing.T) produce.Probe {
			if err := os.Remove(stoppingFile); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			return presence{condition: "risk.shutdown", file: "stopping", every: time.Second}
		}},

		// host.memory-pressure: the line, no line, and unreadable.
		{"host.memory-pressure", big, produce.Fresh, true, func(*testing.T) produce.Probe {
			return memoryPressure{every: time.Second, read: func() (int64, int64, bool, error) { return 99, 100, true, nil }}
		}},
		{"host.memory-pressure", big, produce.Fresh, false, func(*testing.T) produce.Probe {
			return memoryPressure{every: time.Second, read: func() (int64, int64, bool, error) { return 10, 100, true, nil }}
		}},
		{"host.memory-pressure", big, produce.Absent, false, func(*testing.T) produce.Probe {
			return memoryPressure{every: time.Second, read: func() (int64, int64, bool, error) { return 0, 0, false, nil }}
		}},
		{"host.memory-pressure", big, produce.Unknown, false, func(*testing.T) produce.Probe {
			return memoryPressure{every: time.Second, read: func() (int64, int64, bool, error) { return 0, 0, false, os.ErrPermission }}
		}},

		// host.cpu-throttled: the ratio, no controller, and unreadable.
		{"host.cpu-throttled", big, produce.Fresh, true, func(*testing.T) produce.Probe {
			return cpuThrottled{every: time.Second, read: func() (int64, int64, bool, error) { return 50, 100, true, nil }}
		}},
		{"host.cpu-throttled", big, produce.Fresh, false, func(*testing.T) produce.Probe {
			return cpuThrottled{every: time.Second, read: func() (int64, int64, bool, error) { return 1, 100, true, nil }}
		}},
		{"host.cpu-throttled", big, produce.Absent, false, func(*testing.T) produce.Probe {
			return cpuThrottled{every: time.Second, read: func() (int64, int64, bool, error) { return 0, 0, false, nil }}
		}},
		{"host.cpu-throttled", big, produce.Unknown, false, func(*testing.T) produce.Probe {
			return cpuThrottled{every: time.Second, read: func() (int64, int64, bool, error) { return 0, 0, false, os.ErrPermission }}
		}},

		// The two diagnostics that compare the job against its own baseline.
		{"job.step-slow", big, produce.Fresh, true, func(t *testing.T) produce.Probe {
			writePair(t, dir, "step-baseline", "100", "step-rate", "300")
			return stepSlow{}.with(Config{})
		}},
		{"job.step-slow", big, produce.Fresh, false, func(t *testing.T) produce.Probe {
			writePair(t, dir, "step-baseline", "100", "step-rate", "110")
			return stepSlow{}.with(Config{})
		}},
		{"store.slow", big, produce.Fresh, true, func(t *testing.T) produce.Probe {
			writePair(t, dir, "write-baseline", "1000", "write-rate", "2500")
			return storeSlow{}.with(Config{})
		}},
		{"store.slow", big, produce.Fresh, false, func(t *testing.T) produce.Probe {
			writePair(t, dir, "write-baseline", "1000", "write-rate", "1200")
			return storeSlow{}.with(Config{})
		}},

		// risk.eviction-likely: an inference over the world, with four guardrails, so all four
		// answers are reachable.
		// The firing case needs both terms now: pressure *and* somebody higher waiting. A full node with
		// nobody waiting above it took nobody in any of the twelve sweep cases (experiments/kueue).
		{"risk.eviction-likely", big, produce.Fresh, true, func(t *testing.T) produce.Probe {
			return (evictionLikely{}).with(writeWorldAt(t, worldOpts{cpuPct: 92, memPct: 40, pending: 2, tracking: true}, big.Now))
		}},
		{"risk.eviction-likely", big, produce.Fresh, false, func(t *testing.T) produce.Probe {
			return (evictionLikely{}).with(writeWorldAt(t, worldOpts{cpuPct: 10, memPct: 10, tracking: true}, big.Now))
		}},
		{"risk.eviction-likely", big, produce.Absent, false, func(t *testing.T) produce.Probe {
			return (evictionLikely{}).with(writeWorldAt(t, worldOpts{cpuPct: 99, tracking: false}, big.Now))
		}},
		{"risk.eviction-likely", big, produce.Unknown, false, func(t *testing.T) produce.Probe {
			return (evictionLikely{}).with(writeWorldAt(t, worldOpts{cpuPct: 10, tracking: true, age: 10 * time.Minute}, big.Now))
		}},

		// risk.preempt: an announcement, not a guess.
		{"risk.preempt", big, produce.Fresh, true, func(t *testing.T) produce.Probe {
			return (preemption{}).with(writeWorldAt(t, worldOpts{tracking: true, announced: true}, big.Now))
		}},
		{"risk.preempt", big, produce.Fresh, false, func(t *testing.T) produce.Probe {
			return (preemption{}).with(writeWorldAt(t, worldOpts{tracking: true}, big.Now))
		}},
		{"risk.preempt", big, produce.Absent, false, func(t *testing.T) produce.Probe {
			return (preemption{}).with(writeWorldAt(t, worldOpts{tracking: false}, big.Now))
		}},
		{"risk.preempt", big, produce.Unknown, false, func(t *testing.T) produce.Probe {
			return (preemption{}).with(writeWorldAt(t, worldOpts{tracking: true, noUnit: true}, big.Now))
		}},

		// What this build does not have says so, and says nothing else.
		{"host.gpu-fault", big, produce.Absent, false, func(*testing.T) produce.Probe { return notHere{name: "host.gpu-fault", every: time.Second} }},
		{"host.gpu-hot", big, produce.Absent, false, func(*testing.T) produce.Probe { return notHere{name: "host.gpu-hot", every: time.Second} }},
		{"net.long-outage", big, produce.Absent, false, func(*testing.T) produce.Probe { return notHere{name: "net.long-outage", every: time.Second} }},
	}

	type answer struct {
		state produce.State
		value bool
	}
	seen := map[string]map[answer]bool{}
	for _, r := range rows {
		got := r.make(t).Read(r.env)
		if got.State != r.want || (r.want == produce.Fresh && got.Value != r.value) {
			t.Fatalf("%s = %+v, want %v/%v", r.probe, got, r.want, r.value)
		}
		if seen[r.probe] == nil {
			seen[r.probe] = map[answer]bool{}
		}
		seen[r.probe][answer{got.State, got.Value}] = true
	}

	// What each condition promises to be able to say. A new adapter that cannot reach its own
	// states fails here, which is the point: "it compiles" is not "it can report".
	must := map[string][]answer{
		"store.low-space":      {{produce.Fresh, true}, {produce.Fresh, false}, {produce.Unknown, false}},
		"store.blocked":        {{produce.Fresh, true}, {produce.Fresh, false}},
		"job.ending":           {{produce.Fresh, true}, {produce.Fresh, false}},
		"host.memory-pressure": {{produce.Fresh, true}, {produce.Fresh, false}, {produce.Absent, false}, {produce.Unknown, false}},
		"host.cpu-throttled":   {{produce.Fresh, true}, {produce.Fresh, false}, {produce.Absent, false}, {produce.Unknown, false}},
		"risk.preempt":         {{produce.Absent, false}},
		"host.gpu-fault":       {{produce.Absent, false}},
		"host.gpu-hot":         {{produce.Absent, false}},
		"net.long-outage":      {{produce.Absent, false}},
	}
	for name, answers := range must {
		for _, a := range answers {
			if !seen[name][a] {
				t.Errorf("%s never reached %v/%v", name, a.state, a.value)
			}
		}
	}
	t.Logf("probe contract: %d conditions, %d rows, %d states asserted", len(must), len(rows), len(rows))
}

// Every condition that can be declared in the catalogue and is not derived by the sampler must have
// a row above. The list is spelled out so that adding an adapter without a test fails loudly.
func TestEveryAdapterHasAContractRow(t *testing.T) {
	want := map[string]bool{
		"store.low-space": true, "store.blocked": true, "job.ending": true, "risk.shutdown": true,
		"host.memory-pressure": true, "host.cpu-throttled": true,
		"risk.preempt": true, "host.gpu-fault": true, "host.gpu-hot": true, "net.long-outage": true,
		"job.step-slow": true, "store.slow": true, "risk.eviction-likely": true,
	}
	have := map[string]bool{}
	for _, p := range All(Config{}) {
		have[p.Name()] = true
	}
	for name := range want {
		if !have[name] {
			t.Errorf("%s has a contract row but no adapter", name)
		}
	}
	for name := range have {
		if !want[name] {
			t.Errorf("%s has an adapter but no contract row", name)
		}
	}
}

// writePair writes the two numbers a slow condition compares.
func writePair(t *testing.T, dir, a, av, b, bv string) {
	t.Helper()
	base := filepath.Join(dir, ".ckpt-governor")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]string{a: av, b: bv} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
