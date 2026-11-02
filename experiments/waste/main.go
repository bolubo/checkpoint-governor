//go:build ignore

// What the framework defaults cost, per scale. Run with `make waste`.
//
// Every row names the framework default it used and where that number was read. The table is a model:
// the assumptions line is printed first, and a table without it would be a rumour.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/bolubo/checkpoint-governor/internal/waste"
)

func main() {
	hours := flag.Float64("hours", 4, "length of the job in hours")
	bpp := flag.Float64("bytes-per-param", 16, "bytes per parameter (fp16 weight + fp32 master + Adam m/v)")
	tput := flag.Float64("throughput", 2, "aggregate write throughput in GB/s")
	spe := flag.Int("steps-per-epoch", 1000, "steps in one epoch, for the frameworks whose default is 1 epoch")
	fwOnly := flag.String("framework", "", "only this framework (huggingface, lightning, composer, megatron)")
	flag.Parse()

	a := waste.Assumptions{Hours: *hours, BytesPerParam: *bpp, ThroughputGBs: *tput, StepsPerEpoch: *spe}

	fmt.Printf("assumptions: %.0fh job · %.0f bytes/param · %.1f GB/s · %d steps/epoch · step times are illustrative\n",
		a.Hours, a.BytesPerParam, a.ThroughputGBs, a.StepsPerEpoch)
	fmt.Println("the formula: waste fraction = T_write / (interval × t_step), independent of how long the job runs")
	fmt.Println()

	frameworks := waste.Frameworks
	if *fwOnly != "" {
		frameworks = nil
		for _, f := range waste.Frameworks {
			if f.Name == *fwOnly {
				frameworks = append(frameworks, f)
			}
		}
		if len(frameworks) == 0 {
			fmt.Fprintf(os.Stderr, "waste: unknown framework %q\n", *fwOnly)
			os.Exit(2)
		}
	}

	for _, f := range frameworks {
		fmt.Printf("%s — %s\n", f.Name, f.Source)
		fmt.Printf("  %-16s %7s %7s %9s %9s %11s %10s %8s\n",
			"scale", "interval", "writes", "write", "waste", "GPU-hours", "storage", "keeps")
		for _, s := range waste.Scales {
			r, err := waste.Run(a, f, s)
			if err != nil {
				fmt.Fprintln(os.Stderr, "waste:", err)
				os.Exit(1)
			}
			if r.IntervalSteps == 0 {
				fmt.Printf("  %-16s %7s %7s %9s %9s %11s %10s %8s  %s\n",
					r.Scale, "—", "—", "—", "—", "—", "—", "—", r.Note)
				continue
			}
			fmt.Printf("  %-16s %6d %7d %8.0fs %8.1f%% %10.1f %10s %8s",
				r.Scale, r.IntervalSteps, r.Writes, r.WriteSeconds, r.WasteFraction*100,
				r.WasteGPUHours, waste.ShortBytes(r.StorageBytes), r.Keeps)
			if !r.Feasible {
				fmt.Printf("  %s", r.Note)
			}
			fmt.Println()
		}
		fmt.Println()
	}
}
