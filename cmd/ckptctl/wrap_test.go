package main

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeChild is a child that records the signals it was sent, so the protocol can be tested without
// spawning a process. It exits as soon as it is signalled, which is what a real framework does on a
// SIGTERM it understands.
type fakeChild struct {
	mu   sync.Mutex
	sigs []os.Signal
	done chan struct{}
}

func newFake() *fakeChild { return &fakeChild{done: make(chan struct{})} }

func (f *fakeChild) signal(s os.Signal) error {
	f.mu.Lock()
	f.sigs = append(f.sigs, s)
	f.mu.Unlock()
	f.finish()
	return nil
}

func (f *fakeChild) finish() {
	select {
	case <-f.done:
	default:
		close(f.done)
	}
}

func (f *fakeChild) wait() int { <-f.done; return 0 }

func (f *fakeChild) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sigs)
}

func setPoll(d time.Duration) func() {
	old := pollEvery
	pollEvery = d
	return func() { pollEvery = old }
}

// A signal with no grace forwards at once and writes the two files a controller reads.
func TestWrapWithNoGraceForwardsAtOnce(t *testing.T) {
	dir := t.TempDir()
	child, sigs := newFake(), make(chan os.Signal, 2)
	sigs <- syscall.SIGTERM
	var out bytes.Buffer

	if code := supervise(dir, 0, time.Second, child, sigs, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".ckpt-governor", "stopping")); err != nil {
		t.Fatalf("stopping was not written: %v", err)
	}
	if child.count() != 1 {
		t.Fatalf("signals = %d, want one forwarded term", child.count())
	}
	if !bytes.Contains(out.Bytes(), []byte("grace is 0")) {
		t.Fatalf("output does not warn about the zero grace:\n%s", out.String())
	}
}

// The point of the wrapper: it does not forward while a copy is still missing. It waits for a sealed
// copy above the step it saw at the signal, then forwards.
func TestWrapWaitsForASealedCopyThenForwards(t *testing.T) {
	restore := setPoll(30 * time.Millisecond)
	defer restore()
	dir := t.TempDir()
	child, sigs := newFake(), make(chan os.Signal, 2)
	var out bytes.Buffer

	result := make(chan int, 1)
	go func() { result <- supervise(dir, time.Second, time.Second, child, sigs, &out) }()
	sigs <- syscall.SIGTERM

	// A framework write lands, with the completion mark and the declaration a restore needs.
	time.Sleep(60 * time.Millisecond)
	ckpt := filepath.Join(dir, "checkpoint-5")
	if err := os.MkdirAll(ckpt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ckpt, "trainer_state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case code := <-result:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wrapper never forwarded")
	}
	if child.count() != 1 {
		t.Fatalf("signals = %d, want one", child.count())
	}
	if !bytes.Contains(out.Bytes(), []byte("sealed at step 5")) {
		t.Fatalf("output does not say the copy landed:\n%s", out.String())
	}
}

// A deadline is an honest failure, not a silent one — and without a controller it says which glue was
// missing.
func TestWrapDeadlineReportsTheFinalWriteWasNotMade(t *testing.T) {
	restore := setPoll(30 * time.Millisecond)
	defer restore()
	for _, tc := range []struct {
		name       string
		controller bool
		want       string
	}{
		{"with a controller", true, "final write NOT made"},
		{"no controller", false, "final write NOT made: no controller"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.controller {
				if err := writeStopFile(dir, "state.json", `{"schema":2}`); err != nil {
					t.Fatal(err)
				}
			}
			child, sigs := newFake(), make(chan os.Signal, 2)
			var out bytes.Buffer
			result := make(chan int, 1)
			go func() { result <- supervise(dir, 100*time.Millisecond, time.Second, child, sigs, &out) }()
			sigs <- syscall.SIGTERM
			select {
			case code := <-result:
				if code != 0 {
					t.Fatalf("exit %d: %s", code, out.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the wrapper never reached the deadline")
			}
			if !bytes.Contains(out.Bytes(), []byte(tc.want)) {
				t.Fatalf("output does not contain %q:\n%s", tc.want, out.String())
			}
			if _, err := os.Stat(filepath.Join(dir, ".ckpt-governor", "stopping")); err != nil {
				t.Fatalf("stopping was not written: %v", err)
			}
		})
	}
}

// A user who insists gets an immediate stop, and the fact is recorded rather than assumed.
func TestWrapSecondSignalForwardsImmediatelyAndRecordsIt(t *testing.T) {
	restore := setPoll(30 * time.Millisecond)
	defer restore()
	dir := t.TempDir()
	child, sigs := newFake(), make(chan os.Signal, 2)
	var out bytes.Buffer
	result := make(chan int, 1)
	go func() { result <- supervise(dir, time.Minute, time.Second, child, sigs, &out) }()
	sigs <- syscall.SIGTERM
	time.Sleep(40 * time.Millisecond)
	sigs <- syscall.SIGTERM

	select {
	case code := <-result:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second signal did not forward")
	}
	if !bytes.Contains(out.Bytes(), []byte("second signal")) {
		t.Fatalf("output does not say the signal was repeated:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".ckpt-governor", "release-forced")); err != nil {
		t.Fatalf("release-forced was not written: %v", err)
	}
}

// A job that finished on its own is not a job that was stopped: no stopping file, the child's code.
func TestWrapReportsAChildThatExitedOnItsOwn(t *testing.T) {
	dir := t.TempDir()
	child, sigs := newFake(), make(chan os.Signal, 2)
	child.finish()
	var out bytes.Buffer
	if code := supervise(dir, time.Minute, time.Second, child, sigs, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".ckpt-governor", "stopping")); err == nil {
		t.Fatal("a child that exited on its own was reported as stopped")
	}
}

// The window is decided once: an explicit flag wins, the platform's declared grace is read, and
// absence means zero — never a made-up default.
func TestWrapGraceFromDeployment(t *testing.T) {
	dir := t.TempDir()
	if err := writeStopFile(dir, "deployment.json", `{"schema":1,"grace_seconds":60}`); err != nil {
		t.Fatal(err)
	}
	got, source, err := graceWindow(dir, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != 60*time.Second || source != "deployment.json" {
		t.Fatalf("grace = %s from %q, want 60s from deployment.json", got, source)
	}

	// An explicit flag wins, so a job whose platform changed the window is not held by the file.
	got, source, err = graceWindow(dir, 5*time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != 5*time.Second || source != "--grace" {
		t.Fatalf("grace = %s from %q, want 5s from --grace", got, source)
	}

	// Declared zero stays zero: it is an answer, not a missing value.
	if err := writeStopFile(dir, "deployment.json", `{"schema":1,"grace_seconds":0}`); err != nil {
		t.Fatal(err)
	}
	if got, _, err = graceWindow(dir, 0, false); err != nil || got != 0 {
		t.Fatalf("declared zero = %s, %v; want 0", got, err)
	}

	// No file: zero, and the source says why.
	empty := t.TempDir()
	if got, source, err = graceWindow(empty, 0, false); err != nil || got != 0 {
		t.Fatalf("absent file = %s, %v; want 0", got, err)
	}
	if source == "deployment.json" {
		t.Fatalf("source = %q, want the default explanation", source)
	}

	// A negative window is a malformed declaration, refused where it is read.
	if err := writeStopFile(dir, "deployment.json", `{"schema":1,"grace_seconds":-5}`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := graceWindow(dir, 0, false); err == nil {
		t.Fatal("a negative grace_seconds was accepted")
	}
}

// A wrapper inside a wrapper would announce one stop twice.
func TestWrapRefusesToNest(t *testing.T) {
	t.Setenv("CKPTCTL_WRAPPED", "1")
	var out bytes.Buffer
	if code := runWrap([]string{"--dir=" + t.TempDir(), "--", "true"}, &out); code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("already under ckptctl wrap")) {
		t.Fatalf("output does not explain the refusal:\n%s", out.String())
	}
}
