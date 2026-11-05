package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/loop"
)

// The three answers, priced for this job.
//
// The question is "how long will this run take", not "how often do you want to checkpoint", because
// the second one is the question nobody can answer — it is why the number gets picked from a blog
// post. Duration is something a person (or their assistant) knows, and everything else is arithmetic
// against a price we measured.
type initOption struct {
	key    string
	label  string
	writes int
	window time.Duration
}

func runInit(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "the checkpoint directory")
	duration := fs.Duration("duration", 0, "how long the run will take (0 = ask, or unknown)")
	answer := fs.String("answer", "", "none | middle | periodic; skips the question")
	params := fs.String("params", "", "model size (1.5B, 7B, 70B); estimates 18 bytes per parameter for disk only, not a GPU check")
	copies := fs.Int("copies", 1, "the smallest number of copies kept on disk")
	fs.Usage = func() {
		fmt.Fprintln(out, "usage: ckptctl init --dir=<checkpointDir> [--duration=4h] [--answer=none|middle|periodic] [--params=7B] [--copies=1]")
		fmt.Fprintln(out, "       writes numbers.json, or nothing at all when the answer is none")
		fmt.Fprintln(out, "       --params estimates disk only (18 bytes per parameter: fp16 weights, two Adam moments,")
		fmt.Fprintln(out, "       gradient and master weights). It says nothing about whether this GPU can train the model.")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fs.Usage()
		return 2
	}
	if *copies < 1 {
		fmt.Fprintln(out, "error: --copies must be at least 1")
		return 2
	}

	var modelParams float64
	if *params != "" {
		v, ok := parseParams(*params)
		if !ok {
			fmt.Fprintf(out, "error: --params %q is not a model size; write 7B or 700M\n", *params)
			return 2
		}
		modelParams = v
	}

	price := loop.PriceOf(*dir)

	in := bufio.NewReader(os.Stdin)
	interactive := isTerminal(os.Stdin)
	if *duration == 0 && interactive && *answer == "" {
		fmt.Fprint(out, "How long will this run take? (e.g. 4h, empty to skip) ")
		line, _ := in.ReadString('\n')
		line = strings.TrimSpace(line)
		if line != "" {
			if d, err := time.ParseDuration(line); err == nil {
				*duration = d
			} else {
				fmt.Fprintf(out, "  %q is not a duration; continuing without one\n", line)
			}
		}
	}

	opts := []initOption{{key: "none", label: "nothing"}}
	if *duration > 0 {
		opts = append(opts, initOption{key: "middle", label: "one at the middle", writes: 1, window: *duration / 2})
		w := 30 * time.Minute
		if *duration < 2*time.Hour {
			// A thirty-minute window on a fifty-minute run is not a window, it is the whole run.
			w = *duration / 4
		}
		opts = append(opts, initOption{
			key: "periodic", label: "every " + shortDuration(w),
			writes: int(*duration / w), window: w,
		})
	}

	fmt.Fprintf(out, "one write: %s\n", price.Describe())
	fmt.Fprintln(out, diskClause(price, *params, modelParams, *copies))
	fmt.Fprintln(out)
	for _, o := range opts {
		fmt.Fprintf(out, "  %-10s %-18s %-26s %-28s %s\n",
			o.key, o.label, writesText(o, price), diskShort(price, modelParams, *copies), boundText(o))
	}
	fmt.Fprintln(out)

	chosen := *answer
	if chosen == "" {
		if !interactive {
			fmt.Fprintln(out, "nothing written: pass --answer=none|middle|periodic, or run this with a terminal")
			return 0
		}
		fmt.Fprint(out, "which? [none] ")
		line, _ := in.ReadString('\n')
		chosen = strings.TrimSpace(line)
		if chosen == "" {
			chosen = "none"
		}
	}

	var pick *initOption
	for i := range opts {
		if opts[i].key == chosen {
			pick = &opts[i]
		}
	}
	if pick == nil {
		fmt.Fprintf(out, "error: %q is not one of none, middle, periodic\n", chosen)
		return 2
	}

	if pick.key == "none" {
		// Decision (product-v1 §13): skipping writes nothing, so "zero config" keeps meaning
		// what it says. A file saying `no-us=none` would be louder, and would also make the next run
		// look configured when nobody decided anything.
		fmt.Fprintln(out, "nothing written: the run will say `no-us none (unset)` and write once, at the end.")
		return 0
	}

	n := advise.Numbers{WithUs: shortDuration(pick.window)}
	if err := n.Write(*dir); err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "wrote %s/%s: with-us=%s · %s · %s\n",
		advise.BaseDir, advise.NumbersFile, shortDuration(pick.window), pick.label, writesText(*pick, price))
	return 0
}

func writesText(o initOption, p loop.WritePrice) string {
	if o.writes == 0 {
		return "0 writes"
	}
	noun := "writes"
	if o.writes == 1 {
		noun = "write"
	}
	if p.Seconds <= 0 {
		return fmt.Sprintf("%d %s, price unknown until the first", o.writes, noun)
	}
	total := time.Duration(float64(o.writes) * p.Seconds * float64(time.Second))
	return fmt.Sprintf("%d %s, about %s", o.writes, noun, shortDuration(total))
}

func boundText(o initOption) string {
	if o.window == 0 {
		return "a silent death costs everything"
	}
	return "a silent death costs at most " + shortDuration(o.window)
}

// bytesPerParam is the design's flat estimate: fp16 weights (2), two Adam moments in fp32 (8), and
// gradient plus master weights (8). A job that offloads or keeps 8-bit optimizer state will differ,
// which is why every line that uses this says estimated and not measured.
const bytesPerParam = 18

// parseParams accepts a model size written with a unit: 1.5B, 7B, 700M. A bare number is refused on
// purpose — 7 could be seven parameters or seven billion, and a disk figure built on that is a guess
// wearing a measurement's clothes.
func parseParams(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	var mult float64
	switch s[len(s)-1] {
	case 'B', 'b':
		mult = 1e9
	case 'M', 'm':
		mult = 1e6
	default:
		return 0, false
	}
	v, err := strconv.ParseFloat(s[:len(s)-1], 64)
	if err != nil || !(v > 0) || v > 1e6 {
		return 0, false
	}
	return v * mult, true
}

// diskClause answers "how much disk", with its provenance attached: an estimated number and a
// measured one must not look alike.
func diskClause(price loop.WritePrice, params string, parsed float64, copies int) string {
	switch {
	case parsed > 0:
		return fmt.Sprintf("disk: %s × %s (estimated from %s, not measured; disk only, not a GPU check)",
			sizeText(int64(parsed*bytesPerParam)), copiesText(copies), params)
	case price.Bytes > 0:
		return fmt.Sprintf("disk: %s × %s (from the size the job declared)", sizeText(price.Bytes), copiesText(copies))
	}
	return "disk: unknown until the first write"
}

func diskShort(price loop.WritePrice, parsed float64, copies int) string {
	switch {
	case parsed > 0:
		return "disk: " + sizeText(int64(parsed*bytesPerParam)) + " × " + copiesText(copies)
	case price.Bytes > 0:
		return "disk: " + sizeText(price.Bytes) + " × " + copiesText(copies)
	}
	return "disk: unknown"
}

func copiesText(n int) string {
	if n == 1 {
		return "1 copy"
	}
	return fmt.Sprintf("%d copies", n)
}

// sizeText renders binary units. Above a thousand GiB it switches to TiB, divided by 1000 the way a
// person compares a model size to a disk they bought — the number is an estimate either way.
func sizeText(n int64) string {
	gib := float64(n) / (1 << 30)
	switch {
	case gib >= 1000:
		return fmt.Sprintf("%.2f TiB", gib/1000)
	case gib < 1:
		return fmt.Sprintf("%.0f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.0f GiB", gib)
}

// shortDuration trims the noise from Go's duration formatting: "4h0m0s" is a computer talking, and
// "994.803264ms" is a computer shouting. One unit, one decimal.
func shortDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d == 0:
		return "0s"
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d > time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes())) // 90m beats 1h30m in a price column
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}
