package advise

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EventKind is what happened. The list is closed: a new kind is a documentation change, not a
// string invented at a call site, because these lines are the only record of what the controller did
// while nobody was watching.
type EventKind string

const (
	// EventDecision is one decision, with the condition that produced it.
	EventDecision EventKind = "decision"
	// EventPublish is a plan reaching the disk (or being skipped because it did not change).
	EventPublish EventKind = "publish"
	// EventAttemptStart and EventAttemptEnd bracket one write request.
	EventAttemptStart EventKind = "attempt_start"
	EventAttemptEnd   EventKind = "attempt_end"
	// EventCondition is a condition changing state (0→1, 1→0, present→unknown).
	EventCondition EventKind = "condition"
	// EventLease is taking, renewing, losing or releasing the directory lease.
	EventLease EventKind = "lease"
	// EventBackoff is the executor deciding to wait.
	EventBackoff EventKind = "backoff"
	// EventError is something that failed and was handled.
	EventError EventKind = "error"
	// EventConfig is a threshold or cadence an operator changed. It is recorded because a number
	// nobody remembers changing is the same as a number nobody decided.
	EventConfig EventKind = "config"
)

// Kinds is the frozen list of event kinds, in the order they were introduced. A reader that keys on
// `kind` (a log shipper, a dashboard, a script) is reading a promise, not an implementation detail.
func Kinds() []EventKind {
	return []EventKind{EventDecision, EventPublish, EventAttemptStart, EventAttemptEnd,
		EventCondition, EventLease, EventBackoff, EventError, EventConfig}
}

// Event is one line of events.jsonl. Values holds only numbers and short strings: a log line that
// needs a nested object is a decision that should have been recorded as data instead.
type Event struct {
	At     time.Time      `json:"at"`
	Unit   string         `json:"unit,omitempty"`
	Kind   EventKind      `json:"kind"`
	Code   string         `json:"code,omitempty"`
	Values map[string]any `json:"values,omitempty"`
}

const (
	eventsName = "events.jsonl"
	// eventsMaxBytes bounds the file. Events are a record, not an archive: when the cap is reached
	// the oldest lines go, so the file never grows without someone deciding to keep it.
	eventsMaxBytes = 1 << 20
)

func eventsPath(dir string) string { return filepath.Join(dir, BaseDir, eventsName) }

// Record appends one event. A failure to record never fails the caller: losing a log line must not
// stop a write that a condition asked for.
func Record(dir string, e Event) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	path := eventsPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if cerr != nil {
		return cerr
	}
	return trimEvents(path)
}

// trimEvents keeps the newest lines when the file outgrows its cap.
func trimEvents(path string) error {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= eventsMaxBytes {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	keep := raw[len(raw)-eventsMaxBytes:]
	// Start at a line boundary, so a reader never sees half an event.
	if i := strings.IndexByte(string(keep), '\n'); i >= 0 {
		keep = keep[i+1:]
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, keep, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadEvents returns at most limit events, oldest first. A limit of zero means all of them.
func ReadEvents(dir string, limit int) ([]Event, error) {
	f, err := os.Open(eventsPath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue // a torn line is not worth failing a read over
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}
