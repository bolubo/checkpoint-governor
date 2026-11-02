// Package notify posts one event per changed plan to an HTTP endpoint.
//
// It is the outward edge of the delivery plane. The plan file is how the training job learns what
// to do; a webhook is how the rest of a platform learns that something happened, without polling
// every checkpoint directory. Nothing here decides anything: an event that cannot be delivered is
// recorded and the round continues, because a report is not a condition.
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Event is the body of one POST. It is deliberately flat: the same numbers the plan carries, plus
// the identity of the unit they belong to, so a receiver can act without reading the directory.
type Event struct {
	Unit      string    `json:"unit"`
	Cluster   string    `json:"cluster,omitempty"`
	At        time.Time `json:"at"`
	Action    string    `json:"action"`
	Reason    string    `json:"reason"`
	Why       string    `json:"why,omitempty"`
	RequestID string    `json:"requestId,omitempty"`
	Version   string    `json:"version,omitempty"`
}

// DefaultTimeout bounds one delivery. The loop delivers synchronously — a plan change is rare and
// rate-limited — but a receiver that never answers must not become the reason a write is late.
const DefaultTimeout = 2 * time.Second

// Poster delivers events to one URL.
type Poster struct {
	URL    string
	Client *http.Client
}

// New returns a poster with the default timeout.
func New(url string) Poster {
	return Poster{URL: url, Client: &http.Client{Timeout: DefaultTimeout}}
}

// Post delivers one event. Success is any 2xx; everything else is an error the caller records.
func (p Poster) Post(e Event) error {
	if p.URL == "" {
		return fmt.Errorf("notify: no endpoint configured")
	}
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	req, err := http.NewRequest(http.MethodPost, p.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ckptctl")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Read the body so the connection can be reused, and drop it: a receiver's reply is not input.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("notify: endpoint answered %s", resp.Status)
	}
	return nil
}
