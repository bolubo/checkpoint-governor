// Command ckptctl is the command line for checkpoint-governor.
//
// `run` is the controller: it reads one checkpoint directory, decides, and publishes a plan. The
// other commands inspect what the controller did, or price a configuration before it is applied.
// None of them talks to a cluster, and none of them needs one.
package main

import (
	"fmt"
	"os"
)

// version is the release the binary was built from. `make build` and `make release` set it with
// -ldflags "-X main.version=..."; a plain `go build` leaves it at "dev", which is what a test run
// should report rather than a version nobody tagged.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: ckptctl <command>")
		fmt.Fprintln(os.Stderr, "commands: init, version, simulate, table, status, price, audit, serve, run, trace, doctor, events, explain, glue, config, supervise, world, stats, resumable, resumed")
		os.Exit(2)
	}

	switch os.Args[1] {
	case "init":
		if code := runInit(os.Args[2:], os.Stdout); code != 0 {
			os.Exit(code)
		}
	case "version":
		fmt.Println("ckptctl " + version)
	case "simulate":
		if code := runSimulate(os.Args[2:], os.Stdout); code != 0 {
			os.Exit(code)
		}
	case "table":
		if code := runTable(os.Args[2:], os.Stdout); code != 0 {
			os.Exit(code)
		}
	case "status":
		if code := runStatus(os.Args[2:], os.Stdout); code != 0 {
			os.Exit(code)
		}
	case "price":
		if code := runPrice(os.Args[2:], os.Stdout); code != 0 {
			os.Exit(code)
		}
	case "audit":
		if code := runAudit(os.Args[2:], os.Stdout); code != 0 {
			os.Exit(code)
		}
	case "serve":
		if code := runServe(os.Args[2:], os.Stdout); code != 0 {
			os.Exit(code)
		}
	case "supervise":
		if code := runSupervise(os.Args[2:], os.Stdout); code != 0 {
			os.Exit(code)
		}
	case "stats":
		if code := runStats(os.Args[2:], os.Stdout); code != 0 {
			os.Exit(code)
		}
	case "world":
		if code := runWorld(os.Args[2:], os.Stdout); code != 0 {
			os.Exit(code)
		}
	case "config":
		if code := runConfig(os.Args[2:], os.Stdout); code != 0 {
			os.Exit(code)
		}
	case "glue":
		if code := runGlue(os.Args[2:], os.Stdout); code != 0 {
			os.Exit(code)
		}
	case "verify":
		if code := runVerify(os.Args[2:], os.Stdout); code != 0 {
			os.Exit(code)
		}
	case "explain":
		if code := runExplain(os.Args[2:], os.Stdout); code != 0 {
			os.Exit(code)
		}
	case "events":
		if code := runEvents(os.Args[2:], os.Stdout); code != 0 {
			os.Exit(code)
		}
	case "doctor":
		if code := runDoctor(os.Args[2:], os.Stdout); code != 0 {
			os.Exit(code)
		}
	case "run":
		if code := runRun(os.Args[2:], os.Stdout); code != 0 {
			os.Exit(code)
		}
	case "trace":
		if code := runTrace(os.Args[2:], os.Stdout); code != 0 {
			os.Exit(code)
		}
	case "resumable":
		if code := runResumable(os.Args[2:], os.Stdout); code != 0 {
			os.Exit(code)
		}
	case "resumed":
		if code := runResumed(os.Args[2:], os.Stdout); code != 0 {
			os.Exit(code)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}
