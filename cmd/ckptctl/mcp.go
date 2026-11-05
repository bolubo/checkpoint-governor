package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/bolubo/checkpoint-governor/internal/mcp"
)

type rootsFlag []string

func (r *rootsFlag) String() string { return fmt.Sprint([]string(*r)) }

func (r *rootsFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// runMCP is the read-only protocol server. It writes nothing to stdout except JSON-RPC responses: a
// log line on stdout would corrupt the stream an agent is reading.
func runMCP(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var roots rootsFlag
	fs.Var(&roots, "root", "a directory the tools may read (repeatable; required)")
	transport := fs.String("transport", "stdio", "stdio is the only transport in this release")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: ckptctl mcp --root=<checkpointDir> [--root=<dir> ...]")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *transport != "stdio" {
		fmt.Fprintf(os.Stderr, "mcp: transport %q is not supported (only stdio)\n", *transport)
		return 2
	}
	if len(roots) == 0 {
		fs.Usage()
		return 2
	}

	srv := mcp.New([]string(roots), version)
	if err := srv.Serve(os.Stdin, out); err != nil {
		fmt.Fprintf(os.Stderr, "mcp: %v\n", err)
		return 1
	}
	return 0
}
