package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/unit"
)

// The scrape path is what Prometheus will call, so it is driven through ServeHTTP with a recorder
// rather than trusted because Render has a unit test: the wiring is where the labels come from.
func TestTheScrapePathIsReadOnlyAndLabelled(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "job-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	source := func() ([]unit.Member, error) {
		return []unit.Member{{ID: "job-1", Name: "job-1", Dir: dir, Cluster: "kind-a"}}, nil
	}
	h := serveHandler(source, func() time.Time { return time.Now() })

	body := scrape(t, h, "/metrics", http.StatusOK)
	for _, want := range []string{
		"# TYPE checkpoint_staleness_seconds gauge",
		// No copy in this directory: infinite, not zero. A zero would claim a fresh checkpoint.
		`checkpoint_staleness_seconds{unit="job-1",cluster="kind-a"} +Inf`,
		"checkpoint_units 1",
		"checkpoint_clusters 1",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}

	// The scrape must not create anything: it is an observer beside a supervisor, not a writer.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a scrape wrote into the directory: %v", entries)
	}

	// A source that cannot read the roots is a 500, not a page of zeroes.
	broken := serveHandler(func() ([]unit.Member, error) { return nil, os.ErrPermission }, time.Now)
	if code := scrape(t, broken, "/metrics", http.StatusInternalServerError); code == "" {
		t.Fatal("the refusal body was empty")
	}

	if body := scrape(t, h, "/healthz", http.StatusOK); strings.TrimSpace(body) != "ok" {
		t.Fatalf("healthz said %q", body)
	}
}

func scrape(t *testing.T, h http.Handler, path string, want int) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != want {
		t.Fatalf("%s answered %d, want %d", path, rec.Code, want)
	}
	return rec.Body.String()
}
