package verify

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixedTime() time.Time { return time.Date(2026, 11, 2, 3, 20, 7, 0, time.UTC) }

// versionDir finds the single generation directory under a fixture, so a test does not hard-code a name
// that a future fixture would have to match.
func versionDir(t *testing.T, fixture string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("%s: %v", fixture, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			return e.Name()
		}
	}
	t.Fatalf("%s: no generation directory", fixture)
	return ""
}
