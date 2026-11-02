package notify

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// roundTripFunc lets a test answer a request without a socket. The delivery contract is the request
// a receiver will see — method, type, body — and that is what this file pins.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func reply(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// The body is the contract with a receiver: one POST, one flat object, the unit's own name in it.
func TestOnePostCarriesTheEvent(t *testing.T) {
	var got string
	var method, contentType string
	p := Poster{
		URL: "http://receiver.test/hook",
		Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			method, contentType = r.Method, r.Header.Get("Content-Type")
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			got = string(raw)
			return reply(http.StatusOK, ""), nil
		})},
	}

	err := p.Post(Event{
		Unit: "job-1", Cluster: "kind-a", At: time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC),
		Action: "save", Reason: "risk", Why: "the job is being closed", RequestID: "req-1",
		Version: "risk-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"unit":"job-1"`, `"cluster":"kind-a"`, `"action":"save"`, `"requestId":"req-1"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("body %s does not contain %s", got, want)
		}
	}
	if method != http.MethodPost {
		t.Fatalf("method = %s, want POST", method)
	}
	if !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("content type = %q", contentType)
	}
}

// A receiver that refuses is an error, not a silent success: the caller records it.
func TestANonTwoHundredIsAnError(t *testing.T) {
	p := Poster{
		URL: "http://receiver.test/hook",
		Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return reply(http.StatusInternalServerError, "no"), nil
		})},
	}
	if err := p.Post(Event{Unit: "job-1", Action: "save"}); err == nil {
		t.Fatal("a 500 was reported as delivered")
	}
}

// A transport failure (no receiver, no route) is an error too.
func TestATransportFailureIsAnError(t *testing.T) {
	p := Poster{
		URL: "http://receiver.test/hook",
		Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, io.ErrUnexpectedEOF
		})},
	}
	if err := p.Post(Event{Unit: "job-1", Action: "save"}); err == nil {
		t.Fatal("a broken connection was reported as delivered")
	}
}

// An empty endpoint is a configuration mistake, and it must not be a panic at the first write.
func TestNoEndpointIsAnError(t *testing.T) {
	if err := New("").Post(Event{Unit: "job-1"}); err == nil {
		t.Fatal("an empty endpoint was reported as delivered")
	}
}
