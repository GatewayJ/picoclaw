package csgclaw

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

func TestHasInboundBotAtMention(t *testing.T) {
	tests := []struct {
		name    string
		botID   string
		content string
		ok      bool
	}{
		{name: "matching at tag", botID: "u-manager", content: `<at user_id="u-manager">manager</at> hello`, ok: true},
		{name: "other at tag ignored", botID: "u-manager", content: `<at user_id="alice">alice</at> hello`, ok: false},
		{name: "later matching at tag works", botID: "u-manager", content: `<at user_id="alice">alice</at> <at user_id="u-manager">manager</at> hello`, ok: true},
		{name: "plain at text ignored", botID: "u-manager", content: "@manager hello", ok: false},
		{name: "missing quote ignored", botID: "u-manager", content: `<at user_id="u-manager>manager</at>`, ok: false},
		{name: "empty content ignored", botID: "u-manager", content: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok := hasInboundBotAtMention(tt.content, tt.botID)
			if ok != tt.ok {
				t.Fatalf("hasInboundBotAtMention(%q, %q) = %v, want %v", tt.content, tt.botID, ok, tt.ok)
			}
		})
	}
}

func TestNormalizeInboundAtMentions(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "single mention", content: `<at user_id="u-manager">manager</at> hi`, want: `@manager hi`},
		{name: "multiple mentions", content: `<at user_id="alice">alice</at> hi <at user_id="u-manager">manager</at>`, want: `@alice hi @manager`},
		{name: "empty mention name keeps original tag", content: `<at user_id="u-manager"></at> hi`, want: `<at user_id="u-manager"></at> hi`},
		{name: "broken tag keeps tail", content: `<at user_id="u-manager">manager hi`, want: `<at user_id="u-manager">manager hi`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeInboundAtMentions(tt.content)
			if got != tt.want {
				t.Fatalf("normalizeInboundAtMentions(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestHandleInboundEventDirectAlwaysProcesses(t *testing.T) {
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
		Text: "hello",
	})

	select {
	case msg := <-mb.InboundChan():
		if msg.ChatID != "room-1" {
			t.Fatalf("inbound chat ID = %q, want %q", msg.ChatID, "room-1")
		}
		if msg.Content != "hello" {
			t.Fatalf("inbound content = %q, want %q", msg.Content, "hello")
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("timed out waiting for inbound message")
	}
}

func TestHandleInboundEventGroupIgnoresNonBotMention(t *testing.T) {
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
		ChatType:  "group",
		Sender: sender{
			ID: "user-1",
		},
		Text: `<at user_id="alice">alice</at> hello`,
	})

	select {
	case msg := <-mb.InboundChan():
		t.Fatalf("unexpected inbound message published: %+v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHandleInboundEventGroupProcessesBotMention(t *testing.T) {
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
		ChatType:  "group",
		Sender: sender{
			ID: "user-1",
		},
		Text: `<at user_id="u-manager">manager</at> hi`,
	})

	select {
	case msg := <-mb.InboundChan():
		if msg.ChatID != "room-1" {
			t.Fatalf("inbound chat ID = %q, want %q", msg.ChatID, "room-1")
		}
		if msg.Content != `@manager hi` {
			t.Fatalf("inbound content = %q, want %q", msg.Content, `@manager hi`)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("timed out waiting for inbound message")
	}
}

func TestHandleInboundEventThreadUsesTopicChatID(t *testing.T) {
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

	ch.dispatchEvent("message", `{"message_id":"msg-reply","room_id":"room-1","chat_type":"group","thread_root_id":"msg-root","sender":{"id":"user-1"},"text":"<at user_id=\"u-manager\">manager</at> hi","context":{"topic_id":"msg-root"}}`)

	select {
	case msg := <-mb.InboundChan():
		if msg.ChatID != "room-1/msg-root" {
			t.Fatalf("inbound chat ID = %q, want %q", msg.ChatID, "room-1/msg-root")
		}
		if msg.Peer.Kind != "group" {
			t.Fatalf("peer kind = %q, want group", msg.Peer.Kind)
		}
		if msg.Peer.ID != "room-1/msg-root" {
			t.Fatalf("peer ID = %q, want %q", msg.Peer.ID, "room-1/msg-root")
		}
		if msg.Metadata["room_id"] != "room-1" {
			t.Fatalf("metadata room_id = %q, want room-1", msg.Metadata["room_id"])
		}
		if msg.Metadata["topic_id"] != "msg-root" {
			t.Fatalf("metadata topic_id = %q, want msg-root", msg.Metadata["topic_id"])
		}
		if msg.Metadata["parent_peer_kind"] != "topic" {
			t.Fatalf("metadata parent_peer_kind = %q, want topic", msg.Metadata["parent_peer_kind"])
		}
		if msg.Metadata["parent_peer_id"] != "msg-root" {
			t.Fatalf("metadata parent_peer_id = %q, want msg-root", msg.Metadata["parent_peer_id"])
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("timed out waiting for inbound message")
	}
}

func TestSendThreadMessageIncludesTopicContext(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/bots/u-manager/messages/send" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("Unmarshal(%s) error = %v", string(body), err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	mb := bus.NewMessageBus()
	defer mb.Close()

	ch, err := NewChannel(config.CSGClawConfig{
		BaseURL:     server.URL,
		BotID:       "u-manager",
		AccessToken: "secret",
	}, mb)
	if err != nil {
		t.Fatalf("NewChannel() error = %v", err)
	}
	ch.SetRunning(true)

	if err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "room-1/msg-root",
		Content: "thread answer",
	}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if got["room_id"] != "room-1" {
		t.Fatalf("room_id = %v, want room-1; payload=%v", got["room_id"], got)
	}
	if got["text"] != "thread answer" {
		t.Fatalf("text = %v, want thread answer; payload=%v", got["text"], got)
	}
	if got["topic_id"] != "msg-root" {
		t.Fatalf("topic_id = %v, want msg-root; payload=%v", got["topic_id"], got)
	}
	contextPayload, ok := got["context"].(map[string]any)
	if !ok {
		t.Fatalf("context = %T, want object; payload=%v", got["context"], got)
	}
	if contextPayload["chat_id"] != "room-1" {
		t.Fatalf("context.chat_id = %v, want room-1; payload=%v", contextPayload["chat_id"], got)
	}
	if contextPayload["topic_id"] != "msg-root" {
		t.Fatalf("context.topic_id = %v, want msg-root; payload=%v", contextPayload["topic_id"], got)
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
		Text: "hi",
	})

	select {
	case msg := <-mb.InboundChan():
		if msg.ChatID != "room-1" {
			t.Fatalf("inbound chat ID = %q, want %q", msg.ChatID, "room-1")
		}
		if msg.Content != "hi" {
			t.Fatalf("inbound content = %q, want %q", msg.Content, "hi")
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
