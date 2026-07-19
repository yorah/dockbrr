package httpapi

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEventStreamDeliversPublishedEvents(t *testing.T) {
	bus := NewBus()
	s, _, tok, csrf := authedServer(t, Deps{Bus: bus})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/events/stream", nil)
	req = authReq(req, tok, csrf)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}

	// Give the handler a moment to Subscribe before we publish, then emit.
	deadline := time.Now().Add(2 * time.Second)
	got := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "data: ") {
				got <- strings.TrimPrefix(line, "data: ")
				return
			}
		}
	}()

	// Publish repeatedly until the reader picks one up (avoids a subscribe race).
	for {
		bus.Publish(Event{Type: "detected", ServiceID: 3})
		select {
		case line := <-got:
			if !strings.Contains(line, `"type":"detected"`) || !strings.Contains(line, `"service_id":3`) {
				t.Fatalf("unexpected data frame: %q", line)
			}
			return
		case <-time.After(50 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatal("no data frame received before deadline")
			}
		}
	}
}

func TestEventStreamSendsHeartbeat(t *testing.T) {
	orig := heartbeatInterval
	heartbeatInterval = 20 * time.Millisecond
	defer func() { heartbeatInterval = orig }()

	bus := NewBus()
	s, _, tok, csrf := authedServer(t, Deps{Bus: bus})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/events/stream", nil)
	req = authReq(req, tok, csrf)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read frames WITHOUT publishing anything: only a heartbeat comment can arrive.
	got := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, ":") {
				got <- line
				return
			}
		}
	}()
	select {
	case line := <-got:
		if !strings.Contains(line, "heartbeat") {
			t.Fatalf("comment frame = %q, want a heartbeat", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no heartbeat frame received before deadline")
	}
}

func TestEventStreamRequiresAuth(t *testing.T) {
	bus := NewBus()
	s, _, _, _ := authedServer(t, Deps{Bus: bus})
	req := httptest.NewRequest(http.MethodGet, "/api/events/stream", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated stream = %d, want 401", rec.Code)
	}
}

func TestBusPublishDoesNotBlockOnSlowSubscriber(t *testing.T) {
	b := NewBus()
	ch, cancel := b.Subscribe()
	defer cancel()
	for i := 0; i < 100; i++ { // buffer is 16; must not deadlock
		b.Publish(Event{Type: "detected"})
	}
	select {
	case <-ch: // drains at least one
	default:
		t.Fatal("expected at least one buffered event")
	}
}

func TestBusSubscribeCancelStopsDelivery(t *testing.T) {
	b := NewBus()
	ch, cancel := b.Subscribe()
	cancel()
	b.Publish(Event{Type: "detected"})
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("cancelled subscriber still received an event")
		}
	default:
		// no delivery, expected
	}
}
