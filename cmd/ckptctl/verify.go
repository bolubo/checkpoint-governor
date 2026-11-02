package main

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/verify"
)

// runVerify answers the other question: not "why did it not write" (that is `explain`), but "is what
// landed any good".
//
// They are separate commands on purpose. `explain` reads the plan the job was given and explains a
// decision; this reads the disk and reports a state. Mixing them would make both answers worse: one would
// be about a decision and a file at once (0025).
//
//	governor verify --dir=/ckpt/gen-0007 --generation=7
//	governor verify --dir=/ckpt/gen-0007 --generation=7 --full
func runVerify(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	dir := fs.String("dir", "", "the generation directory to check")
	generation := fs.Int64("generation", 0, "the generation that was asked for (0: whatever is there)")
	deadline := fs.String("deadline", "", "RFC3339 deadline from the request; empty means none was announced")
	content := fs.Bool("content", true, "hash the shards; without this the answer can only be sealed")
	full := fs.Bool("full", false, "hash every byte instead of the declared tails")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprintln(out, "verify: --dir is required")
		return 2
	}

	opts := verify.Options{Now: time.Now(), CheckContent: *content, Full: *full}
	if *deadline != "" {
		t, err := time.Parse(time.RFC3339, *deadline)
		if err != nil {
			fmt.Fprintf(out, "verify: --deadline %q is not RFC3339: %v\n", *deadline, err)
			return 2
		}
		opts.Deadline = t
	}

	v := verify.Classify(*dir, *generation, opts)

	// The state and the reason on one line, then the detail on the next: the first line is what a person
	// greps for, the second is what tells them which shard.
	fmt.Fprintf(out, "%s %s\n", v.State, v.Reason)
	if v.Detail != "" {
		fmt.Fprintf(out, "  %s\n", v.Detail)
	}
	if v.State == policy.CopyVerified || v.State == policy.CopySealed {
		if v.Step > 0 {
			fmt.Fprintf(out, "  steps %d\n", v.Step)
		}
	}

	// Exit codes are for the loop that runs this, not for a person reading it: a copy that is not good
	// enough is a failure, while one still being written is not.
	switch v.State {
	case policy.CopyBroken:
		return 1
	default:
		return 0
	}
}
