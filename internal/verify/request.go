package verify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Request is how the product asks for a write without touching the data.
//
// It carries a generation, because a rewrite is a new generation and never a retry of the old one: a slow
// writer that finishes generation 7 after we asked for 8 must not be able to satisfy the newer request
// with the older copy. It carries a deadline, because a request without one cannot be judged late.
type Request struct {
	Generation int64     `json:"generation"`
	Reason     string    `json:"reason"`
	NotAfter   time.Time `json:"notAfter"`
	Full       bool      `json:"full"`
}

// WriteRequest writes the request atomically: a temporary name in the same directory, then a rename.
//
// A half-written request is a request that was never made, and the reader has no way to tell a truncated
// one from a shorter one — so it must never be visible under the real name.
func WriteRequest(path string, r Request) error {
	if r.Generation <= 0 {
		return fmt.Errorf("request generation %d is not positive", r.Generation)
	}
	if r.Reason == "" {
		return fmt.Errorf("request has no reason: a write without a reason is a cost without a cause")
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".request-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once the rename succeeded

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// ReadRequest reads a request and refuses anything incomplete.
//
// DisallowUnknownFields is not pedantry here: a request written by a newer version of us carries fields
// this build does not understand, and silently ignoring them would mean honouring half a decision.
func ReadRequest(path string) (Request, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Request{}, err
	}
	var r Request
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return Request{}, fmt.Errorf("request %s does not parse: %w", path, err)
	}
	if r.Generation <= 0 {
		return Request{}, fmt.Errorf("request %s has generation %d", path, r.Generation)
	}
	return r, nil
}
