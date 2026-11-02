package drill

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MaxOutput bounds what a loader may put into the receipt. A loader that prints a hundred megabytes is
// a loader whose output nobody reads, and the receipt has to stay one screen.
const MaxOutput = 4 << 10

// Result is what one run of a loader left behind.
type Result struct {
	ExitCode  int
	Output    string
	TimedOut  bool
	Step      int64
	StepKnown bool
	Duration  time.Duration
}

// stepLine finds the last `step=<n>` a loader printed. The contract is one line, but a library that logs
// other things first should not fail the drill because of it.
var stepLine = regexp.MustCompile(`(?m)^step=(-?\d+)\s*$`)

// Run executes the loader once, with a deadline and a bounded output.
//
// CKPT_DIR and CKPT_GEN are the whole environment contract: a loader does not have to know where the
// checkpoints are, and we never pass it anything else it might need a secret for.
func Run(ctx context.Context, loader, ckptDir string, generation int64, timeout time.Duration) (Result, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "sh", "-c", loader)
	cmd.Env = append(os.Environ(),
		"CKPT_DIR="+ckptDir,
		fmt.Sprintf("CKPT_GEN=%d", generation),
	)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	started := time.Now()
	err := cmd.Run()
	elapsed := time.Since(started)

	res := Result{Duration: elapsed, Output: clip(buf.String(), MaxOutput)}
	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		res.TimedOut = true
		res.ExitCode = -1
	case err != nil:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			res.ExitCode = exit.ExitCode()
		} else {
			return res, err
		}
	}
	if m := stepLine.FindAllStringSubmatch(res.Output, -1); len(m) > 0 {
		if n, err := strconv.ParseInt(m[len(m)-1][1], 10, 64); err == nil {
			res.Step, res.StepKnown = n, true
		}
	}
	return res, nil
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "…" + s[len(s)-max:]
}

// LastLine is the first line of a failed loader's output, for the receipt: enough to see why, not enough
// to paste a stack trace into a ticket.
func LastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) == 0 {
		return ""
	}
	line := strings.TrimSpace(lines[len(lines)-1])
	if len(line) > 120 {
		line = line[:120] + "…"
	}
	return line
}
