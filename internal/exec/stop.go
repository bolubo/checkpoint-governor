package exec

import (
	"os"
	"path/filepath"
	"time"
)

// MayExitFile is the file the controller writes to tell a stopping job it may go. The job writes
// `stopping` when it is told to stop; this is the other half of that handshake, and it is the
// controller's to write because the controller is the one that knows whether a final write is still
// running.
const MayExitFile = "may-exit"

// Release decides whether a stopping job may exit, and writes the release when it may.
//
// The job asked to stop and is spending its grace period on one final write. Holding it longer than
// that write needs is holding the user hostage; releasing it before the write is done throws the
// write away. So the rule is narrow:
//
//	no write in flight        release: there is nothing left to wait for
//	in flight, deadline gone  release: the write has already failed the deadline that bounded it
//	in flight, still running  wait: this is the write the grace period was for
//
// A failure releases too. A user who pressed stop must not be held because storage is broken — the
// loss gets reported, not paid for with their time.
//
// The job's own claim ("my last write is done") is deliberately not an input: the job cannot see what
// is in flight, and taking its word for a fact only the controller can observe is the mistake this
// handshake exists to avoid. The caller decides when the answer is owed (docs/design.md §7) and
// asks us the narrow question: may the job go *now*?
func Release(dir string, recent []Attempt, now time.Time) (released bool, why string, err error) {
	for _, a := range recent {
		if a.Done() {
			continue
		}
		if a.Deadline.IsZero() || now.Before(a.Deadline) {
			return false, "writing", nil
		}
		if err := writeMayExit(dir); err != nil {
			return false, "deadline passed", err
		}
		return true, "deadline passed", nil
	}
	if err := writeMayExit(dir); err != nil {
		return false, "no write in flight", err
	}
	return true, "no write in flight", nil
}

// Released says whether the release has already been written, so a caller can report "still held"
// without guessing.
func Released(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".ckpt-governor", MayExitFile))
	return err == nil
}

// writeMayExit is idempotent and atomic: the job reads it once and must never see half a file.
func writeMayExit(dir string) error {
	base := filepath.Join(dir, ".ckpt-governor")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(base, "."+MayExitFile+".tmp")
	if err := os.WriteFile(tmp, []byte("1"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(base, MayExitFile))
}
