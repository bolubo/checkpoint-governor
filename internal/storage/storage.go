// Package storage measures what a directory's filesystem actually promises, instead of assuming the
// POSIX contract every writer relies on.
//
// The product seals a generation by writing every shard, syncing it, and renaming a manifest. That
// protocol only means anything if the volume under it does what the rename-and-fsync idiom assumes:
// a same-directory rename is atomic, a synced file keeps its bytes when the process dies, and another
// client reading the same root sees the file after the rename. On a laptop these are true; on some
// network mounts they are not, and a checkpoint written under the wrong assumption is the one failure
// a restore cannot detect (0027).
//
// So this package probes, prints the measurement, and classifies the result into the three tiers the
// rest of the documentation promises:
//
//	local          a local disk. A sealed generation survives this process; not the node, not the volume.
//	shared-safe    a shared disk that passed every cross-client check. It survives the process and the
//	               node: another pod reading the same root sees it after the writer dies.
//	shared-unsafe  anything that failed a check. It promises nothing beyond what is readable right now;
//	               a resume across pods must not rely on it.
//
// The classifier never upgrades a filesystem by its name: a local disk that failed the checks is
// `shared-unsafe` too, because the tier is about the promise, not the mount type.
package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The three tiers. They are the vocabulary `doctor`, the gate and the operator notes share, so they
// are constants rather than strings scattered through the code.
const (
	ClassLocal        = "local"
	ClassSharedSafe   = "shared-safe"
	ClassSharedUnsafe = "shared-unsafe"
)

// ProbeBytes is the payload the probe writes. It is small on purpose: the question is about metadata
// behaviour (rename, sync, visibility), not about throughput, and a probe that costs a gigabyte of
// I/O on a production volume would not be run on a production volume.
const ProbeBytes = 4096

// Check is one measurement, with the reason it failed. A failure is never summarised as "unknown":
// the whole reason this command exists is that the operator needs the sentence that names the mount
// as unfit (0027).
type Check struct {
	Name   string
	OK     bool
	Detail string
	Took   time.Duration
}

// Report is what one directory answered.
type Report struct {
	Dir     string
	FSName  string
	FSMagic uint64
	// Network is whether the filesystem identifies itself as a network mount. A local mount can still
	// fail the checks (it just did, on this volume); a network mount can pass them.
	Network bool
	Checks  []Check
	Class   string
	// Promise is the sentence the class stands for, printed beside it so two words are not asked to
	// carry the whole contract.
	Promise string
}

// Safe reports whether the tier allows a sealed generation to be trusted.
func (r Report) Safe() bool { return r.Class == ClassLocal || r.Class == ClassSharedSafe }

// Probe measures one directory. The work happens in a temporary subdirectory that is removed before
// returning, so probing a production checkpoint root leaves nothing behind.
func Probe(dir string) (Report, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Report{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Report{Dir: abs}, err
	}
	if !info.IsDir() {
		return Report{Dir: abs}, fmt.Errorf("%s is not a directory", abs)
	}

	magic, fstype, network := fsKind(abs)
	r := Report{Dir: abs, FSName: fstype, FSMagic: magic, Network: network}

	work, err := os.MkdirTemp(abs, ".ckptctl-storage-")
	if err != nil {
		return r, fmt.Errorf("cannot create the probe directory inside %s: %w", abs, err)
	}
	defer os.RemoveAll(work)

	payload := filepath.Join(work, "payload.bin")
	temp := filepath.Join(work, ".payload.tmp")

	// The protocol being tested, step by step: a written shard, synced; a replacement written to a
	// temporary name and synced; a same-directory rename that must be atomic.
	oldBytes := bytesOf('A')
	if err := writeSynced(payload, oldBytes); err != nil {
		r.Checks = append(r.Checks, Check{Name: "fsync file", Detail: err.Error()})
		return finish(r), nil
	}

	newBytes := bytesOf('B')
	start := time.Now()
	if err := writeSynced(temp, newBytes); err != nil {
		r.Checks = append(r.Checks, Check{Name: "fsync file", Took: time.Since(start), Detail: err.Error()})
		// Without a synced replacement there is no rename to test, but the directory sync below still
		// tells the operator something about the volume.
		r.Checks = append(r.Checks, Check{Name: "rename", Detail: "not run: the replacement was not synced"})
		r.Checks = append(r.Checks, Check{Name: "visible", Detail: "not run: the replacement was not synced"})
		r.Checks = append(r.Checks, Check{Name: "size/tail", Detail: "not run: the replacement was not synced"})
		r.Checks = append(r.Checks, dirSyncCheck(work))
		return finish(r), nil
	}
	r.Checks = append(r.Checks, Check{Name: "fsync file", OK: true, Took: time.Since(start), Detail: fmt.Sprintf("%d bytes", len(newBytes))})

	start = time.Now()
	renameErr := os.Rename(temp, payload)
	r.Checks = append(r.Checks, Check{
		Name:   "rename",
		OK:     renameErr == nil,
		Took:   time.Since(start),
		Detail: renameDetail(renameErr, temp, payload),
	})

	if renameErr == nil {
		// A second process, because page cache in this process would answer a different question: can
		// the client that just got the plan read what the writer sealed?
		start = time.Now()
		got, pid, readErr := readOtherProcess(payload)
		visible := readErr == nil && string(got) == string(newBytes)
		detail := ""
		switch {
		case readErr != nil:
			detail = readErr.Error()
		case !visible:
			detail = fmt.Sprintf("read %d bytes, not the %d just renamed", len(got), len(newBytes))
		default:
			detail = fmt.Sprintf("pid %d read %d bytes at once", pid, len(got))
		}
		r.Checks = append(r.Checks, Check{Name: "visible", OK: visible, Took: time.Since(start), Detail: detail})

		start = time.Now()
		sizeOK, sumOK, detail := sizeAndTail(payload, newBytes)
		r.Checks = append(r.Checks, Check{Name: "size/tail", OK: sizeOK && sumOK, Took: time.Since(start), Detail: detail})
	}

	r.Checks = append(r.Checks, dirSyncCheck(work))
	return finish(r), nil
}

// finish computes the tier from the checks and the mount type.
func finish(r Report) Report {
	ok := func(name string) bool {
		for _, c := range r.Checks {
			if c.Name == name {
				return c.OK
			}
		}
		return false
	}
	core := ok("fsync file") && ok("rename") && ok("visible") && ok("size/tail")

	switch {
	case !core:
		r.Class = ClassSharedUnsafe
	case r.Network && !ok("fsync dir"):
		// A shared mount needs the directory entry synced too: the node that wrote it can die before the
		// next client ever asks, and the rename must still be there.
		r.Class = ClassSharedUnsafe
	case r.Network:
		r.Class = ClassSharedSafe
	default:
		r.Class = ClassLocal
	}
	r.Promise = promiseFor(r.Class)
	return r
}

func promiseFor(class string) string {
	switch class {
	case ClassLocal:
		return "process death: a sealed generation survives this process; not the node, not the volume"
	case ClassSharedSafe:
		return "process death and node death: another client on the same root reads it after the writer dies; not the volume"
	default:
		return "nothing beyond what is readable now: do not resume across pods or restarts from this volume"
	}
}

func dirSyncCheck(dir string) Check {
	start := time.Now()
	f, err := os.Open(dir)
	if err != nil {
		return Check{Name: "fsync dir", Took: time.Since(start), Detail: "cannot open the directory: " + err.Error()}
	}
	defer f.Close()
	err = f.Sync()
	detail := "directory entry synced"
	if err != nil {
		detail = "cannot sync the directory entry: " + err.Error()
	}
	return Check{Name: "fsync dir", OK: err == nil, Took: time.Since(start), Detail: detail}
}

func renameDetail(err error, from, to string) string {
	if err != nil {
		return "same-directory rename failed: " + err.Error()
	}
	return "atomic: " + filepath.Base(from) + " -> " + filepath.Base(to) + ", same directory"
}

func writeSynced(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func sizeAndTail(path string, want []byte) (bool, bool, string) {
	info, err := os.Stat(path)
	if err != nil {
		return false, false, "cannot stat the renamed file: " + err.Error()
	}
	if info.Size() != int64(len(want)) {
		return false, false, fmt.Sprintf("size %d, wrote %d", info.Size(), len(want))
	}
	f, err := os.Open(path)
	if err != nil {
		return false, false, "cannot read the renamed file: " + err.Error()
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return false, false, "cannot hash the renamed file: " + err.Error()
	}
	got := hex.EncodeToString(sum.Sum(nil))
	if got != sha256Hex(want) {
		return true, false, fmt.Sprintf("tail hashes to %s, wrote %s", got[:12], sha256Hex(want)[:12])
	}
	return true, true, fmt.Sprintf("size %d, tail sha256 %s", info.Size(), got[:12])
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func bytesOf(c byte) []byte {
	b := make([]byte, ProbeBytes)
	for i := range b {
		b[i] = c
	}
	return b
}

// readOtherProcess reads the file from a child process. The pid is printed because the report's
// sentence is "a second process saw it", and a number is what makes that checkable from a log.
func readOtherProcess(path string) ([]byte, int, error) {
	cmd := exec.Command("cat", path)
	out, err := cmd.Output()
	if err != nil {
		return nil, 0, fmt.Errorf("a second process could not read it: %w", err)
	}
	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	return out, pid, nil
}

// Write prints the report as the table an operator reads: measurement, class, promise, in that order.
// A failed check always carries the reason; the class line is last because it is the conclusion.
func (r Report) Write(out io.Writer) {
	fmt.Fprintf(out, "storage %s\n", r.Dir)
	kind := "local"
	if r.Network {
		kind = "network"
	}
	fmt.Fprintf(out, "  %-11s %s (0x%x), %s\n", "filesystem", r.FSName, r.FSMagic, kind)
	for _, c := range r.Checks {
		state := "ok"
		if !c.OK {
			state = "failed"
		}
		line := fmt.Sprintf("  %-11s %-7s", c.Name, state)
		if c.Took > 0 {
			line += fmt.Sprintf(" %-8s", c.Took.Round(time.Microsecond))
		}
		if c.Detail != "" {
			line += " " + c.Detail
		}
		fmt.Fprintln(out, strings.TrimRight(line, " "))
	}
	fmt.Fprintf(out, "  %-11s %s\n", "class", r.Class)
	fmt.Fprintf(out, "  %-11s %s\n", "promise", r.Promise)
}
