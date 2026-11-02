// Command ckptctl is the command line for checkpoint-governor.
//
// `run` is the controller: it reads one checkpoint directory, decides, and publishes a plan. The
// other commands inspect what the controller did, or price a configuration before it is applied.
// None of them talks to a cluster, and none of them needs one.
package main

import (
	"fmt"
	"io"
	"os"
)

// version is the release the binary was built from. `make build` and `make release` set it with
// -ldflags "-X main.version=..."; a plain `go build` leaves it at "dev", which is what a test run
// should report rather than a version nobody tagged.
var version = "dev"

// The three groups of the help screen. They name what the person is doing, not how the code is
// organised: a verb that fits none of them does not belong on this screen yet.
const (
	groupLoop  = "the loop — it decides"
	groupRead  = "what happened — it reads what the controller wrote"
	groupOther = "everything else"
)

// command is one verb. main dispatches from this table and the help prints from it, so a verb cannot
// exist without a line that names it: a command nobody can find is a command nobody uses.
type command struct {
	name  string
	group string
	one   string
	run   func(args []string, out io.Writer) int
}

// commands is the whole surface, alphabetical inside each group. The first group is the three verbs a
// new user needs, which is why the groups are not ordered by size.
var commands = []command{
	// The loop: the controller, the file it writes, and the price of that file.
	{"init", groupLoop, "answer the two questions and write them down, without running the controller", runInit},
	{"price", groupLoop, "what one bound costs on this directory", runPrice},
	{"run", groupLoop, "govern one directory until interrupted", runRun},

	// Reading back what the controller wrote, on a stopped controller if needed.
	{"audit", groupRead, "the bound certificate; non-zero when the bound was broken", runAudit},
	{"config", groupRead, "every threshold in force, and where it came from", runConfig},
	{"doctor", groupRead, "what each probe sees on this machine", runDoctor},
	{"drill", groupRead, "prove a checkpoint resumes: one loader step, compared", runDrill},
	{"events", groupRead, "the controller's own record: decisions, requests, changes", runEvents},
	{"explain", groupRead, "why the last decision was what it was, from the state file", runExplain},
	{"glue", groupRead, "what the job told us about its size and its stop", runGlue},
	{"resumable", groupRead, "can this be resumed: 0 yes, 1 no, 2 cannot decide", runResumable},
	{"resumed", groupRead, "did it resume, from the job's own report", runResumed},
	{"stats", groupRead, "what this deployment wrote, what it cost, what failed", runStats},
	{"status", groupRead, "what the controller knows about one directory", runStatus},
	{"verify", groupRead, "is what landed sound: one generation, or all of them", runVerify},

	// Beyond one directory: several directories, a cluster, or a trace that stands in for both.
	{"serve", groupOther, "the same facts as Prometheus text, read-only", runServe},
	{"simulate", groupOther, "the policy against fixed timers, on one trace", runSimulate},
	{"supervise", groupOther, "several directories, one rollup", runSupervise},
	{"table", groupOther, "the evidence table: every family under the controllers worth comparing", runTable},
	{"trace", groupOther, "generate a trace, seeded so the same run replays", runTrace},
	{"version", groupOther, "print the version this binary was built from", func(_ []string, out io.Writer) int {
		fmt.Fprintln(out, "ckptctl "+version)
		return 0
	}},
	{"world", groupOther, "what the implementation knows about the cluster; can fabricate one", runWorld},
}

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	for _, c := range commands {
		if c.name == os.Args[1] {
			if code := c.run(os.Args[2:], os.Stdout); code != 0 {
				os.Exit(code)
			}
			return
		}
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
	fmt.Fprintln(os.Stderr, "run ckptctl with no arguments for the three groups")
	os.Exit(2)
}

// usage prints one line per verb, grouped by what the person is doing. It is deliberately short: the
// flags are in `ckptctl <verb>`. The three group headings are checked by a test, because this screen
// is the first thing a user sees and a verb missing from it has to be a compile-time intention.
func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: ckptctl <command>")
	for _, g := range []string{groupLoop, groupRead, groupOther} {
		fmt.Fprintf(w, "\n%s\n", g)
		for _, c := range commands {
			if c.group == g {
				fmt.Fprintf(w, "  %-11s %s\n", c.name, c.one)
			}
		}
	}
}
