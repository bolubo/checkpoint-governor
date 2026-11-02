package loop

import (
	"os"
	"path/filepath"
	"testing"
)

// Both layouts count, because both are written by real writers (F11).
//
// The receipt says how many checkpoints exist "whoever wrote them", and it said zero while a `gen-` directory
// sat on disk with a sealed manifest — the one sentence this product must never get wrong.
func TestASealedGenerationIsCounted(t *testing.T) {
	dir := t.TempDir()

	write := func(name string, sealed bool) {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if sealed {
			if err := os.WriteFile(filepath.Join(path, "manifest.json"), []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	write("step-1", true)         // the older writer
	write("gen-0002", true)       // the contract writer (0025)
	write("gen-0003", false)      // a write in flight: not sealed, so not counted
	write(".ckpt-governor", true) // not a checkpoint at all, whatever it holds

	if got := countSealed(dir); got != 2 {
		t.Fatalf("countSealed = %d, want 2 (step-1 and gen-0002)", got)
	}
}
