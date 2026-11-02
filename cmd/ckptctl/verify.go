package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
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
	all := fs.Bool("all", false, "walk every generation under --dir instead of checking one")
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

	if *all {
		return runVerifyAll(*dir, *generation, opts, out)
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
	// Files the manifest does not name: not a failure, but the only trace of a second writer in the same
	// directory, and silence about them is how a verified generation hides debris.
	if len(v.Extras) > 0 {
		fmt.Fprintf(out, "  %d unlisted: %s\n", len(v.Extras), strings.Join(v.Extras, " "))
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

// runVerifyAll is the "was it worth running" view: one line per generation, then the two numbers.
//
// It does not invent the scheduler's counter. "Taken" is a fact about units that were evicted, which a
// directory cannot know, so the ratio is only printed when a caller supplies it — an invented denominator
// would read as a measurement.
func runVerifyAll(root string, generation int64, opts verify.Options, out io.Writer) int {
	s, err := verify.Summarize(root, generation, opts)
	if err != nil {
		fmt.Fprintf(out, "verify: %v\n", err)
		return 2
	}
	for _, r := range s.Receipts {
		fmt.Fprintln(out, r.Line())
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "generations %d · verified %d · broken %d · unchecked %d\n",
		len(s.Receipts), s.Tally.Verified, s.Tally.Invalid, s.Tally.Unchecked)
	if s.Tally.Verified+s.Tally.Invalid > 0 {
		fmt.Fprintf(out, "invalid rate %.2f%%  (must be zero: a copy recorded as good and later found broken)\n",
			s.Tally.InvalidRate()*100)
	}

	// A walk that found broken generations fails the command, so a script notices.
	for _, v := range s.Verdicts {
		if v.State == policy.CopyBroken {
			return 1
		}
	}
	return 0
}
