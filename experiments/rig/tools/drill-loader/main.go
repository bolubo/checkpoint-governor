//go:build ignore

// Command drill-loader is the synthetic loader CI drills with.
//
// It reads the manifest a generation was sealed with and reports the next step, so the drill's machinery
// (fingerprints, environment, timeout, step judgement) is exercised on every push without torch. A real
// loader — `contrib/ckptctl-pytorch/examples/drill_loader.py` — takes the same contract.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/bolubo/checkpoint-governor/internal/verify"
)

func main() {
	dir := os.Getenv("CKPT_DIR")
	if dir == "" {
		fmt.Fprintln(os.Stderr, "drill-loader: CKPT_DIR is not set")
		os.Exit(2)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "drill-loader:", err)
		os.Exit(1)
	}
	m, err := verify.Parse(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "drill-loader:", err)
		os.Exit(1)
	}
	fmt.Printf("step=%d\n", m.Step+1)
}
