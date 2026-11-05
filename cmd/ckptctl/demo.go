package main

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/demo"
	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/storage"
	"github.com/bolubo/checkpoint-governor/internal/verify"
)

// runDemo is the thirty-second version of the product: a real two-rank generation, one real fault,
// and the same commands that would judge a production copy. It writes under --dir (default
// /tmp/ckpt-demo) and never needs a cluster, a GPU, or a training loop.
//
// It returns 0 even when the copy is broken: the broken copy is the demonstration, not a failure of
// the command.
func runDemo(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "/tmp/ckpt-demo", "where the synthetic checkpoint is written")
	scenario := fs.String("scenario", string(demo.ScenarioKill), "kill | torn | short | clean")
	repair := fs.Bool("repair", false, "rewrite the broken shard and seal the generation again")
	clean := fs.Bool("clean", false, "remove the demo directory")
	asJSON := fs.Bool("json", false, "print the report as one JSON object")
	fs.Usage = func() {
		fmt.Fprintln(out, "usage: ckptctl demo [--dir=/tmp/ckpt-demo] [--scenario=kill|torn|short|clean]")
		fmt.Fprintln(out, "       ckptctl demo --repair --dir=<dir>   rewrite the shard and seal again")
		fmt.Fprintln(out, "       ckptctl demo --clean --dir=<dir>    remove the demo directory")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *clean {
		if err := demo.Clean(*dir); err != nil {
			fmt.Fprintf(out, "demo: %v\n", err)
			return 1
		}
		if *asJSON {
			fmt.Fprintf(out, `{"schema":1,"command":"demo","removed":%q}`+"\n", *dir)
			return 0
		}
		fmt.Fprintf(out, "demo: removed %s\n", *dir)
		return 0
	}

	sc, err := demo.ParseScenario(*scenario)
	if err != nil {
		fmt.Fprintf(out, "demo: %v\n", err)
		return 2
	}

	var f demo.Fixture
	if *repair {
		f, err = demo.Repair(*dir)
	} else {
		f, err = demo.Create(*dir, sc)
	}
	if err != nil {
		fmt.Fprintf(out, "demo: %v\n", err)
		return 1
	}

	report := inspect(f, *repair)
	if *asJSON {
		printDemoJSON(out, report)
		return 0
	}
	printDemo(out, report)
	return 0
}

type demoReport struct {
	Dir        string
	Scenario   string
	Fault      string
	Generation int64
	Step       int64
	Resume     string
	ResumeExit int
	Verify     verify.Verdict
	Storage    storage.Report
	StorageErr error
	Problem    string
	Repaired   bool
}

func inspect(f demo.Fixture, repaired bool) demoReport {
	r := demoReport{
		Dir: f.Dir, Scenario: string(f.Scenario), Fault: f.Fault,
		Generation: demo.Generation, Step: demo.Step, Repaired: repaired,
	}

	var buf stringWriter
	r.ResumeExit = runResumable([]string{"--dir", f.Dir}, &buf)
	r.Resume = trimLine(buf.String())

	r.Verify = verify.Classify(f.GenDir, 0, verify.Options{Now: time.Now(), CheckContent: true})
	if r.Verify.State != policy.CopyVerified {
		r.Problem = r.Verify.Detail
	} else if r.ResumeExit != 0 {
		r.Problem = r.Resume
	}

	if s, err := storage.Probe(f.Dir); err != nil {
		r.StorageErr = err
	} else {
		r.Storage = s
	}
	return r
}

type stringWriter struct{ b []byte }

func (w *stringWriter) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	return len(p), nil
}

func (w *stringWriter) String() string { return string(w.b) }

func trimLine(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func printDemo(out io.Writer, r demoReport) {
	class, promise := storageLine(r)
	fmt.Fprintf(out, "demo %s: 2 ranks, gen-%04d, step %d, storage %s\n",
		r.Dir, r.Generation, r.Step, class)
	fmt.Fprintf(out, "fault: %s\n", r.Fault)
	fmt.Fprintf(out, "status      newest sealed step %d · writing none\n", r.Step)
	fmt.Fprintf(out, "resumable   %s\n", r.Resume)
	fmt.Fprintf(out, "verify      %s: %s\n", r.Verify.State, r.Verify.Detail)
	fmt.Fprintf(out, "evidence    %s · storage %s (%s)\n", problemsLine(r), class, promise)
	next := "ckptctl resumable --dir=" + r.Dir
	if r.Problem != "" {
		next = "ckptctl demo --repair --dir=" + r.Dir
	}
	fmt.Fprintf(out, "next        %s\n", next)
}

func storageLine(r demoReport) (class, promise string) {
	if r.StorageErr != nil || r.Storage.Class == "" {
		return "unknown", "the storage probe failed"
	}
	return r.Storage.Class, shortPromise(r.Storage.Class)
}

// shortPromise is the demo's version of the storage promise: the full sentence lives in
// `doctor --storage` and the operator page, while the demo needs a phrase that fits on one line.
func shortPromise(class string) string {
	switch class {
	case storage.ClassLocal:
		return "process death"
	case storage.ClassSharedSafe:
		return "process and node death"
	default:
		return "nothing beyond what is readable now"
	}
}

func problemsLine(r demoReport) string {
	if r.Problem == "" {
		return "0 problems"
	}
	return "1 problem"
}

func printDemoJSON(out io.Writer, r demoReport) {
	class, promise := storageLine(r)
	resumeState := "resumable"
	if r.ResumeExit != 0 {
		resumeState = "not resumable"
	}
	problems := []string{}
	if r.Problem != "" {
		problems = append(problems, r.Problem)
	}
	fmt.Fprintf(out, `{"schema":1,"command":"demo","dir":%q,"scenario":%q,"fault":%q,`+
		`"generation":%d,"step":%d,`+
		`"resume":{"state":%q,"line":%q,"exitCode":%d},`+
		`"verify":{"state":%q,"reason":%q,"detail":%q},`+
		`"storage":{"class":%q,"promise":%q},`+
		`"problems":%v}`+"\n",
		r.Dir, r.Scenario, r.Fault, r.Generation, r.Step,
		resumeState, r.Resume, r.ResumeExit,
		r.Verify.State, r.Verify.Reason, r.Verify.Detail,
		class, promise, quoteList(problems))
}

func quoteList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	s := "["
	for i, item := range items {
		if i > 0 {
			s += ","
		}
		s += fmt.Sprintf("%q", item)
	}
	return s + "]"
}
