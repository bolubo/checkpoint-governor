// Package main is the observer: one process per cluster that reads the platform and writes the world.
//
// It makes no decisions. It collapses what the platform knows into one summary per cluster plus one
// tiny record per unit, and the controller reads those files. Living in its
// own module keeps the dependency on client-go away from the core binary, which has to run on a
// laptop, on Slurm, and on bare metal.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/bolubo/checkpoint-governor/contrib/observer/world"
)

func main() {
	fs := flag.NewFlagSet("ckpt-observer", flag.ExitOnError)
	worldDir := fs.String("world-dir", "", "where to write the world (required)")
	cluster := fs.String("cluster", "", "cluster name to write under (default: the kubeconfig context)")
	every := fs.Duration("every", 5*time.Second, "how often to read the cluster")
	once := fs.Bool("once", false, "read once, write once, exit")
	units := fs.String("units", "labeled", "which pods get a unit record: labeled|all|none")
	unitLabel := fs.String("unit-label", "ckpt-governor.io/unit", "pods with this label are units")
	staleAfter := fs.Int("stale-after-seconds", 30, "how long a reader may believe the summary")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if *worldDir == "" {
		fmt.Fprintln(os.Stderr, "usage: ckpt-observer --world-dir=<dir> [--cluster=a] [--every=5s] [--once]")
		os.Exit(2)
	}

	cfg, name, err := kubeConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "observer: %v\n", err)
		os.Exit(1)
	}
	if *cluster != "" {
		name = *cluster
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "observer: %v\n", err)
		os.Exit(1)
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		dyn = nil
	}

	opts := world.Options{
		Cluster: name, StaleAfter: time.Duration(*staleAfter) * time.Second,
		Units: *units, UnitLabel: *unitLabel, Dynamic: dyn,
	}

	for {
		snap, err := world.Read(client, opts, time.Now())
		if err != nil {
			// A failed read must not replace a good summary with a broken one: the reader turns
			// staleness into `unknown`, which is the honest answer, and this line says why.
			fmt.Fprintf(os.Stderr, "observer: read failed: %v\n", err)
			if *once {
				os.Exit(1)
			}
			time.Sleep(*every)
			continue
		}
		if err := world.Write(*worldDir, snap); err != nil {
			fmt.Fprintf(os.Stderr, "observer: write failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("observer: cluster %s, %d nodes, %d units, wrote %s\n",
			snap.Summary.Cluster, len(snap.Summary.Nodes), len(snap.Units), *worldDir)
		if *once {
			return
		}
		time.Sleep(*every)
	}
}

// kubeConfig prefers the in-cluster service account, then the kubeconfig in the usual places. The
// name it returns is what the summary is written under when nobody passed --cluster.
func kubeConfig() (cfg *rest.Config, name string, err error) {
	if c, err := rest.InClusterConfig(); err == nil {
		return c, "in-cluster", nil
	}
	loading := clientcmd.NewDefaultClientConfigLoadingRules()
	client := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loading, &clientcmd.ConfigOverrides{})
	c, err := client.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("no cluster credentials: %w", err)
	}
	raw, _ := client.RawConfig()
	name = raw.CurrentContext
	if name == "" {
		name = "cluster"
	}
	return c, name, nil
}
