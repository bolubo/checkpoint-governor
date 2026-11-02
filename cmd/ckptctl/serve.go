package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/audit"
	"github.com/bolubo/checkpoint-governor/internal/metrics"
	"github.com/bolubo/checkpoint-governor/internal/observe"
	"github.com/bolubo/checkpoint-governor/internal/unit"
)

// runServe exposes what the directories already say as Prometheus text. It is read-only by design:
// it does not decide, does not publish, does not take a lease, so it can run beside a supervisor.
//
// Every scrape re-reads the directories. That is one walk per request rather than a cache, which
// keeps the exporter honest about the moment it measured and means a job that appears is visible on
// the next scrape without a restart.
func runServe(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	addr := fs.String("addr", "127.0.0.1:9090", "listen address")
	members := fs.String("members", "", "checkpoint directories, comma-separated")
	membersFrom := fs.String("members-from", "", "roots whose immediate children are checkpoint directories; "+
		"comma-separated, and `cluster=root` names the world each root reads")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: ckptctl serve --addr=:9090 --members=<dir,...> | --members-from=<root,...>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	explicit := splitMembers(*members)
	var roots []membersRoot
	if *membersFrom != "" {
		roots = parseRoots(*membersFrom)
	}
	if len(explicit) == 0 && len(roots) == 0 {
		fs.Usage()
		return 2
	}

	source := func() ([]unit.Member, error) {
		var ms []unit.Member
		for _, d := range explicit {
			ms = append(ms, unit.Member{ID: d, Name: d, Dir: d})
		}
		if len(roots) > 0 {
			found, err := discoverAll(roots)
			if err != nil {
				return nil, err
			}
			ms = append(ms, found...)
		}
		return ms, nil
	}

	srv := &http.Server{Addr: *addr, Handler: serveHandler(source, time.Now), ReadHeaderTimeout: 5 * time.Second}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	fmt.Fprintf(out, "serving metrics on %s (read-only)\n", *addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(out, "error: %v\n", err)
		return 1
	}
	return 0
}

// serveHandler is the whole HTTP surface, separated from the listener so a test can drive it with
// httptest: the scrape path is the part that can be wrong, and it is the part a person will
// configure in Prometheus before they ever trust the numbers.
func serveHandler(source func() ([]unit.Member, error), now func() time.Time) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		ms, err := source()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		samples := make([]metrics.Sample, 0, len(ms))
		for _, m := range ms {
			samples = append(samples, sampleOf(m, now()))
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprint(w, metrics.Render(samples))
	})
	return mux
}

func sampleOf(m unit.Member, now time.Time) metrics.Sample {
	s := metrics.Sample{Unit: m.ID, Cluster: m.Cluster, Writes: map[string]int{}}
	snap, _, err := observe.Scan(observe.Request{Dir: m.Dir, UnitID: m.ID, Now: now})
	if err != nil {
		return s
	}
	led, err := advise.Load(m.Dir)
	if err != nil {
		return s
	}
	s.Writes = led.Status.Kinds

	seals := make([]time.Time, 0, len(snap.Copies))
	var newest time.Time
	for _, c := range snap.Copies {
		seal := c.CreatedAt.Truncate(time.Second)
		seals = append(seals, seal)
		if seal.After(newest) {
			newest = seal
		}
	}
	if !newest.IsZero() {
		s.Covered = true
		s.Staleness = now.Sub(newest)
	}

	start := time.Unix(led.Status.StartedAt, 0)
	if led.Status.StartedAt == 0 {
		start = time.Unix(led.UnitStartedAt, 0)
	}
	bound := time.Duration(led.Status.WindowSpanSeconds) * time.Second
	if bound > 0 && !start.IsZero() && start.Unix() > 0 {
		s.OverBound = audit.Certify(bound, start, now, seals).OverBound
	}

	if p, ok := advise.ReadPublished(m.Dir); ok {
		s.Blind = len(p.Decision.Missing)
	}
	return s
}
