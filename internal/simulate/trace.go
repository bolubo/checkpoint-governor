// Package simulate replays a recorded trace through a policy and reports the numbers the promise
// is made of: how long the newest checkpoint was too old, and how much of the time was spent
// writing.
//
// A trace describes the world, not a schedule: how fast steps ran, how much space was left, when a
// job was killed, and how long a write took. The schedule under test is the one being simulated, so
// the trace's own saves are read for one thing only — the cost of a write.
package simulate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Kind is one event of a trace.
type Kind string

const (
	KindStep      Kind = "step"
	KindSaveStart Kind = "save_start"
	KindSaveEnd   Kind = "save_end"
	KindFree      Kind = "free"
	KindKill      Kind = "kill"
)

// Event is one line of a trace (policy, "Trace format").
type Event struct {
	T      time.Time
	Kind   Kind
	Step   int64
	Bytes  int64
	Sealed bool
}

// Trace is a parsed trace, in the order it was recorded.
type Trace struct {
	Events []Event
}

// Encode writes one JSON object per line, in the order recorded.
func Encode(tr Trace, w io.Writer) error {
	buf := bufio.NewWriter(w)
	for _, e := range tr.Events {
		raw := struct {
			T      string `json:"t"`
			Event  Kind   `json:"event"`
			Step   int64  `json:"step,omitempty"`
			Bytes  int64  `json:"bytes,omitempty"`
			Sealed bool   `json:"sealed,omitempty"`
		}{T: e.T.UTC().Format(time.RFC3339Nano), Event: e.Kind, Step: e.Step, Bytes: e.Bytes, Sealed: e.Sealed}
		line, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		if _, err := buf.Write(line); err != nil {
			return err
		}
		if err := buf.WriteByte('\n'); err != nil {
			return err
		}
	}
	return buf.Flush()
}

// Parse reads one JSON object per line. A malformed line is an error that names the line: a trace
// that silently drops events produces a plausible table for the wrong world.
func Parse(r io.Reader) (Trace, error) {
	var tr Trace
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Bytes()
		if len(text) == 0 {
			continue
		}
		var raw struct {
			T      string `json:"t"`
			Event  string `json:"event"`
			Step   int64  `json:"step"`
			Bytes  int64  `json:"bytes"`
			Sealed bool   `json:"sealed"`
		}
		if err := json.Unmarshal(text, &raw); err != nil {
			return Trace{}, fmt.Errorf("line %d: %w", line, err)
		}
		t, err := time.Parse(time.RFC3339Nano, raw.T)
		if err != nil {
			return Trace{}, fmt.Errorf("line %d: time %q: %w", line, raw.T, err)
		}
		kind := Kind(raw.Event)
		switch kind {
		case KindStep, KindSaveStart, KindSaveEnd, KindFree, KindKill:
		default:
			return Trace{}, fmt.Errorf("line %d: unknown event %q", line, raw.Event)
		}
		tr.Events = append(tr.Events, Event{
			T: t, Kind: kind, Step: raw.Step, Bytes: raw.Bytes, Sealed: raw.Sealed,
		})
	}
	if err := sc.Err(); err != nil {
		return Trace{}, err
	}
	return tr, nil
}
