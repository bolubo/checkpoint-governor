// Package waste answers a question the frameworks cannot: what does their default checkpoint interval
// cost on a real job? The answer is a model, not a measurement, and it prints its assumptions so the
// reader can replace every one of them with their own number.
//
// The interval and the retention policy are the frameworks' actual defaults, not round numbers:
//
//	Hugging Face  save_strategy="steps", save_steps=500, save_total_limit=None  (keep every copy)
//	Lightning     one checkpoint per epoch (every_n_train_steps=None, every_n_epochs=None), save_top_k=1
//	Composer      save_interval="1ep", num_checkpoints_to_keep=-1                 (keep every copy)
//	Megatron      no default: --save requires an explicit --save-interval
//
// The formula is one line: waste fraction = T_write / (interval × t_step). It is independent of how
// long the job runs, which is why the table can be read by anyone with three numbers.
package waste

import (
	"fmt"
	"math"
)

// Framework is one training framework's default behaviour.
type Framework struct {
	Name string
	// IntervalSteps is the default save interval in steps. Zero means the framework has no default.
	IntervalSteps func(stepsPerEpoch int) int
	// Keeps is "all" when the default retention is unbounded, or a number of copies.
	Keeps string
	// Source says where the number was read. A row without a source is not allowed.
	Source string
}

// Frameworks are the defaults this package models, read from their source on 2026-11-02.
var Frameworks = []Framework{
	{
		Name:          "huggingface",
		IntervalSteps: func(int) int { return 500 },
		Keeps:         "all",
		Source:        "transformers/src/transformers/training_args.py: save_strategy=\"steps\", save_steps=500, save_total_limit=None",
	},
	{
		Name:          "lightning",
		IntervalSteps: func(stepsPerEpoch int) int { return stepsPerEpoch },
		Keeps:         "1",
		Source:        "lightning/pytorch/callbacks/model_checkpoint.py: every_n_train_steps=None and every_n_epochs=None mean once per epoch; save_top_k=1",
	},
	{
		Name:          "composer",
		IntervalSteps: func(stepsPerEpoch int) int { return stepsPerEpoch },
		Keeps:         "all",
		Source:        "composer/callbacks/checkpoint_saver.py: save_interval=\"1ep\", num_checkpoints_to_keep=-1",
	},
	{
		Name:          "megatron",
		IntervalSteps: nil,
		Keeps:         "",
		Source:        "megatron/training/arguments.py: --save requires an explicit --save-interval; there is no default",
	},
}

// Scale is one job: its model size, its world, and how long one step takes. The step times are
// illustrative — they are not benchmarks of any particular GPU.
type Scale struct {
	Name   string
	Params float64 // parameters
	GPUs   int
	StepMS float64
}

// Scales are the four sizes the table shows first.
var Scales = []Scale{
	{Name: "100M / 1 GPU", Params: 100e6, GPUs: 1, StepMS: 500},
	{Name: "1B / 8 GPU", Params: 1e9, GPUs: 8, StepMS: 1000},
	{Name: "7B / 64 GPU", Params: 7e9, GPUs: 64, StepMS: 2000},
	{Name: "70B / 512 GPU", Params: 70e9, GPUs: 512, StepMS: 4000},
}

// Assumptions are the numbers a reader must replace with their own; the printer refuses to show a
// table without them.
type Assumptions struct {
	Hours         float64
	BytesPerParam float64
	ThroughputGBs float64
	StepsPerEpoch int
}

// Row is one framework on one scale.
type Row struct {
	Framework     string
	Source        string
	Scale         string
	IntervalSteps int
	Writes        int
	WriteSeconds  float64
	// WasteFraction is the share of the job's time spent writing checkpoints that, for a job that
	// completes, are never read.
	WasteFraction float64
	// WasteGPUHours is WasteFraction × GPUs × Hours.
	WasteGPUHours float64
	// StorageBytes is what the default retention keeps on disk at the end of the job.
	StorageBytes float64
	Keeps        string
	// Feasible is false when one write takes longer than the interval: the framework then writes
	// continuously, and the job never finishes.
	Feasible bool
	Note     string
}

// Run builds every framework's row for one scale.
func Run(a Assumptions, fw Framework, s Scale) (Row, error) {
	if a.Hours <= 0 || a.BytesPerParam <= 0 || a.ThroughputGBs <= 0 || s.StepMS <= 0 {
		return Row{}, fmt.Errorf("waste: hours, bytes-per-param, throughput and step-ms must be positive")
	}
	if fw.Source == "" {
		return Row{}, fmt.Errorf("waste: %s has no source; a default without a source is a rumour", fw.Name)
	}

	row := Row{Framework: fw.Name, Source: fw.Source, Scale: s.Name, Keeps: fw.Keeps}
	if fw.IntervalSteps == nil {
		row.Note = "no default: the interval is an explicit argument"
		return row, nil
	}

	interval := fw.IntervalSteps(a.StepsPerEpoch)
	if interval <= 0 {
		return Row{}, fmt.Errorf("waste: %s produced a non-positive interval; set --steps-per-epoch", fw.Name)
	}
	stepSeconds := s.StepMS / 1000
	steps := a.Hours * 3600 / stepSeconds
	bytes := s.Params * a.BytesPerParam
	writeSeconds := bytes / (a.ThroughputGBs * 1e9)

	row.IntervalSteps = interval
	row.Writes = int(math.Floor(steps / float64(interval)))
	row.WriteSeconds = writeSeconds
	row.WasteFraction = writeSeconds / (float64(interval) * stepSeconds)
	row.WasteGPUHours = row.WasteFraction * float64(s.GPUs) * a.Hours
	row.Feasible = writeSeconds < float64(interval)*stepSeconds
	if !row.Feasible {
		row.Note = "infeasible: one write takes longer than the interval, so the job never stops writing"
	}
	switch fw.Keeps {
	case "all":
		row.StorageBytes = float64(row.Writes) * bytes
	case "":
		row.StorageBytes = 0
	default:
		// "1" keeps the newest copy (and, in Lightning, the best one, which is also one copy).
		row.StorageBytes = bytes
	}
	return row, nil
}

// ShortBytes renders a byte count the way the table needs it.
func ShortBytes(b float64) string {
	switch {
	case b >= 1e12:
		return fmt.Sprintf("%.1f TB", b/1e12)
	case b >= 1e9:
		return fmt.Sprintf("%.0f GB", b/1e9)
	case b >= 1e6:
		return fmt.Sprintf("%.0f MB", b/1e6)
	default:
		return fmt.Sprintf("%.0f B", b)
	}
}
