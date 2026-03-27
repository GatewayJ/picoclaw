package csgclaw

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestChannelReconnectsSSEWithBackoff(t *testing.T) {
	oldInitial := sseReconnectInitialBackoff
	oldMax := sseReconnectMaxBackoff
	sseReconnectInitialBackoff = 10 * time.Millisecond
	sseReconnectMaxBackoff = 40 * time.Millisecond
	defer func() {
		sseReconnectInitialBackoff = oldInitial
		sseReconnectMaxBackoff = oldMax
	}()

	var eventAttempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/bots/test-bot/events":
			attempt := eventAttempts.Add(1)
			if attempt == 2 || attempt == 3 {
				http.Error(w, "service restarting", http.StatusServiceUnavailable)
				return
			}

			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("response writer does not implement http.Flusher")
			}

			payload := fmt.Sprintf(`{"message_id":"msg-%d","chat_id":"chat-1","chat_type":"direct","sender":{"id":"user-1","username":"alice","display_name":"Alice"},"text":"hello-%d","timestamp":"2026-03-26T00:00:00Z"}`, attempt, attempt)
			_, _ = fmt.Fprintf(w, "event: message\n")
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		case "/api/bots/test-bot/messages/send":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	mb := bus.NewMessageBus()
	defer mb.Close()

	ch, err := NewChannel(config.CSGClawConfig{
		BaseURL:     server.URL,
		BotID:       "test-bot",
		AccessToken: "secret",
	}, mb)
	if err != nil {
		t.Fatalf("NewChannel() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ch.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() {
		_ = ch.Stop(context.Background())
	}()

	msg1 := waitInboundMessage(t, mb.InboundChan(), 500*time.Millisecond)
	if msg1.Content != "hello-1" {
		t.Fatalf("first inbound content = %q, want %q", msg1.Content, "hello-1")
	}

	msg2 := waitInboundMessage(t, mb.InboundChan(), 2*time.Second)
	if msg2.Content != "hello-4" {
		t.Fatalf("second inbound content = %q, want %q", msg2.Content, "hello-4")
	}

	if got := eventAttempts.Load(); got < 4 {
		t.Fatalf("event connection attempts = %d, want at least 4", got)
	}
	if !ch.IsRunning() {
		t.Fatal("channel should remain running after reconnect")
	}
}

func TestStripInboundMentionPrefix(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "space separated prefix", content: "@manager good day, isn't it", want: "good day, isn't it"},
		{name: "colon separated prefix", content: "@alice: hello", want: "hello"},
		{name: "full width separators", content: "＠小助理：你好", want: "你好"},
		{name: "comma separated prefix", content: "@bob, hello", want: "hello"},
		{name: "not at start", content: "hello @manager", want: "hello @manager"},
		{name: "no prefix", content: "hello", want: "hello"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripInboundMentionPrefix(tt.content); got != tt.want {
				t.Fatalf("stripInboundMentionPrefix(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func waitInboundMessage(t *testing.T, ch <-chan bus.InboundMessage, timeout time.Duration) bus.InboundMessage {
	t.Helper()

	select {
	case msg := <-ch:
		return msg
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for inbound message after %v", timeout)
		return bus.InboundMessage{}
	}
}
