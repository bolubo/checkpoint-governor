// Package main is the gate: the one place a arrival passes through before the scheduler can see it.
//
// The diagnosis in decision 0030 is a negative result. A scheduler that preempts in 0.2 s gives an
// inference no lead time; the victim learns it was taken at the same moment it is taken. The arrival,
// on the other hand, is under somebody's control. So the gate holds the submission, asks the victim
// to spend the write it would have to spend anyway, waits for the seal, and only then lets the job
// into the cluster.
//
// It is a separate module for the same reason the observer is: client-go stays out of the core
// binary, which has to run on a laptop, on Slurm and on bare metal.
package main

import (
	"fmt"
	"os"
)

// version is set by the release build (-X main.version), like the two core binaries. It is a
// variable rather than a literal so a downloaded gate can say which release it came from.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "submit":
		os.Exit(runSubmit(os.Args[2:]))
	case "gc":
		os.Exit(runGC(os.Args[2:]))
	case "version":
		fmt.Printf("ckptgate %s\n", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "ckptgate: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `ckptgate — hold a higher-priority arrival until the victim has written

  ckptgate submit --manifest=<job.yaml> --world=<dir> (--cluster=<name> | --queue=<name>)
                   [--ckpt-root=<dir>] [--deadline=2m] [--namespace=<ns>] [--dry-run]

  ckptgate gc --world=<dir> --cluster=<name>

submit reads the manifest but creates nothing until the victims have sealed, or the deadline
passes. gc marks a hold whose gate died as "uncovered": it was never returned, and the platform
carried on without it.

The flag names match the world the observer writes, so the two processes share one directory:
  --world   the same --world-dir the observer writes
  --cluster the same --cluster the observer writes
`)
}
