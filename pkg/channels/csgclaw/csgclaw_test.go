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

			payload := fmt.Sprintf(`{"message_id":"msg-%d","room_id":"room-1","chat_type":"direct","sender":{"id":"user-1","username":"alice","display_name":"Alice"},"text":"@test-bot hello-%d","timestamp":"2026-03-26T00:00:00Z"}`, attempt, attempt)
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
	if msg1.Content != "@test-bot hello-1" {
		t.Fatalf("first inbound content = %q, want %q", msg1.Content, "@test-bot hello-1")
	}

	msg2 := waitInboundMessage(t, mb.InboundChan(), 2*time.Second)
	if msg2.Content != "@test-bot hello-4" {
		t.Fatalf("second inbound content = %q, want %q", msg2.Content, "@test-bot hello-4")
	}

	if got := eventAttempts.Load(); got < 4 {
		t.Fatalf("event connection attempts = %d, want at least 4", got)
	}
	if !ch.IsRunning() {
		t.Fatal("channel should remain running after reconnect")
	}
}

func TestIsFirstInboundBotMentionSelf(t *testing.T) {
	tests := []struct {
		name    string
		botID   string
		content string
		ok      bool
	}{
		{name: "space separated mention", botID: "u-manager", content: "@manager good day, isn't it", ok: true},
		{name: "full bot id also matches", botID: "u-manager", content: "@u-manager hello", ok: true},
		{name: "colon separated mention", botID: "alice", content: "@alice: hello", ok: true},
		{name: "full width mention", botID: "小助理", content: "＠小助理：你好", ok: true},
		{name: "comma separated mention", botID: "bob", content: "@bob, hello", ok: true},
		{name: "mention in middle also matches", botID: "u-manager", content: "hello @manager", ok: true},
		{name: "opening paren before mention", botID: "u-manager", content: "(@manager) hello", ok: true},
		{name: "empty token after at ignored", botID: "u-manager", content: "@ hello @manager", ok: false},
		{name: "punctuation after at ignored", botID: "u-manager", content: "@: hello @manager", ok: false},
		{name: "email local part ignored", botID: "u-manager", content: "a@manager.com", ok: false},
		{name: "inline text before at ignored", botID: "u-manager", content: "foo@manager hello", ok: false},
		{name: "first mention must be self", botID: "u-manager", content: "@alice hello @manager", ok: false},
		{name: "first mention self wins", botID: "u-manager", content: "@alice hello @manager @manager", ok: false},
		{name: "self first with later others", botID: "u-manager", content: "@manager hello @alice", ok: true},
		{name: "other mention ignored", botID: "u-manager", content: "@alice hello", ok: false},
		{name: "no mention ignored", botID: "u-manager", content: "hello", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok := isFirstInboundBotMentionSelf(tt.content, tt.botID)
			if ok != tt.ok {
				t.Fatalf("isFirstInboundBotMentionSelf(%q, %q) = %v, want %v", tt.content, tt.botID, ok, tt.ok)
			}
		})
	}
}

func TestHandleInboundEventIgnoresNonBotMentions(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	ch, err := NewChannel(config.CSGClawConfig{
		BaseURL:     "http://127.0.0.1:18080",
		BotID:       "u-manager",
		AccessToken: "secret",
	}, mb)
	if err != nil {
		t.Fatalf("NewChannel() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch.ctx = ctx

	ch.handleInboundEvent(eventPayload{
		MessageID: "msg-1",
		RoomID:    "room-1",
		ChatType:  "direct",
		Sender: sender{
			ID: "user-1",
		},
		Text: "@alice hello",
	})

	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("unexpected inbound message published: %+v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHandleInboundEventConsumesRoomIDPayload(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	ch, err := NewChannel(config.CSGClawConfig{
		BaseURL:     "http://127.0.0.1:18080",
		BotID:       "u-manager",
		AccessToken: "secret",
	}, mb)
	if err != nil {
		t.Fatalf("NewChannel() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch.ctx = ctx

	ch.handleInboundEvent(eventPayload{
		MessageID: "msg-1",
		RoomID:    "room-1",
		ChatType:  "direct",
		Sender: sender{
			ID: "user-1",
		},
		Text: "@manager hi",
	})

	select {
	case msg := <-mb.InboundChan():
		if msg.ChatID != "room-1" {
			t.Fatalf("inbound chat ID = %q, want %q", msg.ChatID, "room-1")
		}
		if msg.Content != "@manager hi" {
			t.Fatalf("inbound content = %q, want %q", msg.Content, "@manager hi")
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("timed out waiting for inbound message")
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
