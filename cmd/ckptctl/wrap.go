package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/layout"
	"github.com/bolubo/checkpoint-governor/internal/produce/adapters"
)

// runWrap runs the job under a supervisor that makes the stop protocol effective from our side.
//
// A framework callback only answers "should I save?" and returns; the framework then exits on its own
// schedule and never waits for `may-exit`. So on a real host the handshake in `ckptctl run` has no
// executor. The wrapper is the only form that does: the parent takes the signal, writes `stopping`,
// waits until a copy is actually on disk, and only then forwards the signal.
//
// It writes `stopping` and never `may-exit`: the controller still owns that file, and a second writer
// would be a second answer to one question (0026).
func runWrap(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("wrap", flag.ContinueOnError)
	dir := fs.String("dir", "", "the checkpoint directory the controller governs")
	grace := fs.Duration("grace", 0, "the shutdown window the platform gave the job (0 = do not wait)")
	teardown := fs.Duration("teardown", 5*time.Second, "how long the child may take after the forwarded signal")
	fs.SetOutput(out)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if *dir == "" || len(rest) == 0 {
		fmt.Fprint(out, "usage: ckptctl wrap --dir=<checkpointDir> [--grace=60s] [--teardown=5s] -- <command...>\n")
		return 2
	}
	// A wrapper inside a wrapper would write `stopping` twice and wait for two different answers.
	if os.Getenv("CKPTCTL_WRAPPED") != "" {
		fmt.Fprintln(out, "error: this command is already under ckptctl wrap")
		return 2
	}

	cmd := exec.Command(rest[0], rest[1:]...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	cmd.Env = append(os.Environ(), "CKPTCTL_WRAPPED=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(out, "error: cannot start %s: %v\n", rest[0], err)
		return 2
	}

	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)

	strict := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "grace" {
			strict = true
		}
	})
	window, source, err := graceWindow(*dir, *grace, strict)
	if err != nil {
		fmt.Fprintf(out, "wrap: cannot read deployment.json: %v\n", err)
		return 2
	}
	fmt.Fprintf(out, "wrap: grace %s from %s\n", secondsString(window), source)

	return supervise(*dir, window, *teardown, groupChild{cmd: cmd, pgid: cmd.Process.Pid}, sigs, out)
}

// graceWindow is the one place the shutdown window is decided. The platform's own declaration is the
// second source: repeating `--grace` on every command line when `deployment.json` already says it invites
// two different numbers. An explicit flag wins, a declared zero stays zero, an absent declaration is 0.
func graceWindow(dir string, flagValue time.Duration, flagSet bool) (time.Duration, string, error) {
	if flagSet {
		if flagValue < 0 {
			return 0, "", fmt.Errorf("a grace cannot be negative (%s)", flagValue)
		}
		return flagValue, "--grace", nil
	}
	d, err := adapters.LoadDeployment(dir)
	if err != nil {
		return 0, "", err
	}
	if d.HasGrace {
		return time.Duration(d.GraceSeconds) * time.Second, "deployment.json", nil
	}
	return 0, "default (no --grace, no deployment.json)", nil
}

// secondsString is how the wrapper says a window: whole seconds when it can, the duration otherwise.
func secondsString(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	return d.String()
}

// wrapChild is the half of a child process the supervisor uses, so the decision logic can be tested
// without sending a real signal to a real process.
type wrapChild interface {
	signal(os.Signal) error
	wait() int
}

type groupChild struct {
	cmd  *exec.Cmd
	pgid int
}

func (c groupChild) signal(s os.Signal) error {
	// The whole group: a launcher's own children are part of the job's stop.
	return syscall.Kill(-c.pgid, s.(syscall.Signal))
}

func (c groupChild) wait() int {
	_ = c.cmd.Wait()
	if c.cmd.ProcessState == nil {
		return 1
	}
	if code := c.cmd.ProcessState.ExitCode(); code >= 0 {
		return code
	}
	// Killed by a signal: the shell's own convention, so a caller script sees the same number.
	if status, ok := c.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return 1
}

// pollEvery is how often the wrapper looks for a new sealed copy. A wrapper that waits for a disk
// write does not need to be faster than the write.
var pollEvery = 200 * time.Millisecond

// supervise is the protocol in one place. Every branch prints, because a wrapper that waits and says
// nothing is indistinguishable from a wrapper that is stuck.
func supervise(dir string, grace, teardown time.Duration, child wrapChild, sigs <-chan os.Signal, out io.Writer) int {
	exited := make(chan int, 1)
	go func() { exited <- child.wait() }()

	// The job may finish on its own before anything asks it to stop.
	select {
	case code := <-exited:
		return code
	case <-sigs:
	}

	baseline := newestStep(dir)
	started := time.Now()
	if err := writeStopFile(dir, "stopping", "1"); err != nil {
		fmt.Fprintf(out, "wrap: cannot write stopping: %v\n", err)
	}
	left := int(grace.Seconds())
	if left < 0 {
		left = 0
	}
	_ = writeStopFile(dir, "seconds-left", fmt.Sprint(left))
	fmt.Fprintf(out, "wrap: signal received, stopping written, waiting up to %s\n", grace)

	if grace <= 0 {
		fmt.Fprintln(out, "wrap: grace is 0 — forwarding at once; the final write is the job's own")
		return forward(child, teardown, exited)
	}

	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	deadline := started.Add(grace)
	for {
		select {
		case <-sigs:
			// The user insists: the protocol ends here, and the fact is recorded.
			_ = writeStopFile(dir, "release-forced", "1")
			fmt.Fprintln(out, "wrap: second signal → forwarding now")
			return forward(child, teardown, exited)

		case code := <-exited:
			return code

		case now := <-ticker.C:
			if step, ok := sealedAfter(dir, baseline); ok {
				fmt.Fprintf(out, "wrap: sealed at step %d (%s) → forwarding SIGTERM\n",
					step, now.Sub(started).Round(time.Millisecond))
				return forward(child, teardown, exited)
			}
			_ = writeStopFile(dir, "seconds-left", fmt.Sprint(maxInt(0, int(time.Until(deadline).Seconds()))))
			if !now.Before(deadline) {
				if !hasController(dir) {
					fmt.Fprintln(out, "wrap: deadline reached with no new copy → forwarding SIGTERM; final write NOT made: no controller")
				} else {
					fmt.Fprintln(out, "wrap: deadline reached with no new copy → forwarding SIGTERM; final write NOT made")
				}
				return forward(child, teardown, exited)
			}
		}
	}
}

// forward sends SIGTERM to the child, gives it `teardown` to leave, then kills it. The exit code is
// the child's, because a wrapper that invented its own number would hide the job's own failure.
func forward(child wrapChild, teardown time.Duration, exited <-chan int) int {
	_ = child.signal(syscall.SIGTERM)
	select {
	case code := <-exited:
		return code
	case <-time.After(teardown):
		_ = child.signal(syscall.SIGKILL)
		return <-exited
	}
}

// newestStep is the newest sealed copy's step, or 0. The baseline must come from the same finder every
// other command uses, or the wrapper would compare against a copy the receipt never saw.
func newestStep(dir string) int64 {
	cfg, _, err := layout.LoadConfig(dir)
	if err != nil {
		return 0
	}
	res, err := layout.Scan(dir, cfg)
	if err != nil || !res.Found {
		return 0
	}
	return res.Newest.Step
}

// sealedAfter reports a sealed copy with a step above the baseline — the write the stop was for.
func sealedAfter(dir string, baseline int64) (int64, bool) {
	cfg, _, err := layout.LoadConfig(dir)
	if err != nil {
		return 0, false
	}
	res, err := layout.Scan(dir, cfg)
	if err != nil || !res.Found || res.Newest.Step <= baseline {
		return 0, false
	}
	return res.Newest.Step, true
}

// hasController reports whether a plan exists, which is the only evidence that a live controller is
// listening to `stopping`. Without one, the honest answer is "the final write was not made".
func hasController(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".ckpt-governor", "state.json"))
	return err == nil
}

// writeStopFile writes one file of the job's own directory, atomically: a reader must never see half a
// number, and a half-written `stopping` is a stop nobody announced.
func writeStopFile(dir, name, content string) error {
	base := filepath.Join(dir, ".ckpt-governor")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	path := filepath.Join(base, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
