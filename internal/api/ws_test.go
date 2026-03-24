package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deckhand-for-cnpg/deckhand/internal/store"
	"github.com/gorilla/websocket"
)

func TestServeWS(t *testing.T) {
	var logBuffer wsSafeBuffer
	logger := slog.New(slog.NewJSONHandler(&logBuffer, nil))
	st := store.New()
	hub := NewWSHub(logger, st)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hubErrCh := make(chan error, 1)
	go func() {
		hubErrCh <- hub.Start(ctx)
	}()
	waitForWSHubReady(t, hub)

	server := httptest.NewServer(NewRouter(ServerDeps{Logger: logger, Store: st, WebSocketHub: hub}))
	defer server.Close()

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(server.URL)+"/ws", nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("Dial(/ws) error: %v (status=%d)", err, status)
	}
	defer conn.Close()

	waitForLogSubstring(t, &logBuffer, "websocket client connected")

	if err := st.UpsertCluster(apiTestCluster("team-a", "alpha")); err != nil {
		t.Fatalf("UpsertCluster(team-a/alpha) error: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var message WSChangeEvent
	if err := conn.ReadJSON(&message); err != nil {
		t.Fatalf("ReadJSON(): %v", err)
	}

	if got, want := message.Type, "store.changed"; got != want {
		t.Fatalf("message.Type = %q, want %q", got, want)
	}
	if got, want := message.Kind, store.ResourceKindCluster; got != want {
		t.Fatalf("message.Kind = %q, want %q", got, want)
	}
	if got, want := message.Action, store.ActionUpsert; got != want {
		t.Fatalf("message.Action = %q, want %q", got, want)
	}
	if got, want := message.Namespace, "team-a"; got != want {
		t.Fatalf("message.Namespace = %q, want %q", got, want)
	}
	if got, want := message.Name, "alpha"; got != want {
		t.Fatalf("message.Name = %q, want %q", got, want)
	}
	if message.OccurredAt.IsZero() {
		t.Fatal("message.OccurredAt is zero")
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("conn.Close(): %v", err)
	}
	waitForLogSubstring(t, &logBuffer, "websocket client disconnected")
	waitForLogSubstring(t, &logBuffer, "\"path\":\"/ws\"")

	cancel()
	select {
	case err := <-hubErrCh:
		if err != nil {
			t.Fatalf("hub.Start() error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for hub to stop")
	}
}

func TestWSHubDropsSlowClients(t *testing.T) {
	var logBuffer wsSafeBuffer
	logger := slog.New(slog.NewJSONHandler(&logBuffer, nil))
	st := store.New()
	hub := NewWSHub(logger, st)
	hub.clientBuffer = 1

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hubErrCh := make(chan error, 1)
	go func() {
		hubErrCh <- hub.Start(ctx)
	}()
	waitForWSHubReady(t, hub)

	slow := &wsClient{hub: hub, send: make(chan []byte, 1), remoteAddr: "slow"}
	fast := &wsClient{hub: hub, send: make(chan []byte, 1), remoteAddr: "fast"}
	hub.register <- slow
	hub.register <- fast
	waitForLogSubstring(t, &logBuffer, "\"remote_addr\":\"fast\"")

	fastEvents := make(chan WSChangeEvent, 2)
	go func() {
		for message := range fast.send {
			var event WSChangeEvent
			if err := json.Unmarshal(message, &event); err == nil {
				fastEvents <- event
			}
		}
	}()

	if err := st.UpsertCluster(apiTestCluster("team-a", "alpha")); err != nil {
		t.Fatalf("UpsertCluster(team-a/alpha) error: %v", err)
	}
	waitForChannelLen(t, slow.send, 1)

	firstFast := waitForFastEvent(t, fastEvents)
	if got, want := firstFast.Name, "alpha"; got != want {
		t.Fatalf("first fast event name = %q, want %q", got, want)
	}

	if err := st.UpsertCluster(apiTestCluster("team-a", "bravo")); err != nil {
		t.Fatalf("UpsertCluster(team-a/bravo) error: %v", err)
	}

	secondFast := waitForFastEvent(t, fastEvents)
	if got, want := secondFast.Name, "bravo"; got != want {
		t.Fatalf("second fast event name = %q, want %q", got, want)
	}
	waitForLogSubstring(t, &logBuffer, "dropped slow websocket client")

	if _, ok := <-slow.send; !ok {
		t.Fatal("expected slow client to receive its buffered event before closure")
	}
	if _, ok := <-slow.send; ok {
		t.Fatal("expected slow client channel to be closed after drop")
	}

	cancel()
	select {
	case err := <-hubErrCh:
		if err != nil {
			t.Fatalf("hub.Start() error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for hub to stop")
	}
}

func waitForWSHubReady(t *testing.T, hub *WSHub) {
	t.Helper()

	select {
	case <-hub.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for websocket hub readiness")
	}
}

func waitForFastEvent(t *testing.T, events <-chan WSChangeEvent) WSChangeEvent {
	t.Helper()

	select {
	case event := <-events:
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fast client event")
	}
	return WSChangeEvent{}
}

func waitForChannelLen[T any](t *testing.T, ch chan T, want int) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(ch) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for channel len %d; got %d", want, len(ch))
}

func waitForLogSubstring(t *testing.T, buf *wsSafeBuffer, want string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for log substring %q in %s", want, buf.String())
}

func wsURL(serverURL string) string {
	return "ws" + strings.TrimPrefix(serverURL, "http")
}

type wsSafeBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *wsSafeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *wsSafeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

var _ io.Writer = (*wsSafeBuffer)(nil)
