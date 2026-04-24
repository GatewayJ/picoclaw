package csgclaw

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestBotAPIURLPreservesBaseQueryAndPath(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	ch, err := NewChannel(config.CSGClawConfig{
		BaseURL:     "http://127.0.0.1:8080/v1?name=foo",
		BotID:       "test-bot",
		AccessToken: "secret",
	}, mb)
	if err != nil {
		t.Fatalf("NewChannel() error = %v", err)
	}

	got := ch.botAPIURL("/events")
	want := "http://127.0.0.1:8080/v1/api/bots/test-bot/events?name=foo"
	if got != want {
		t.Fatalf("botAPIURL() = %q, want %q", got, want)
	}
}
