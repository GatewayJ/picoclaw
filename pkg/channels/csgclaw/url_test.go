package csgclaw

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestParticipantAPIURLPreservesBaseQueryAndPath(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	ch, err := NewChannel(config.CSGClawConfig{
		BaseURL:       "http://127.0.0.1:8080/v1?name=foo",
		ParticipantID: "test-bot",
		AccessToken:   "secret",
	}, mb)
	if err != nil {
		t.Fatalf("NewChannel() error = %v", err)
	}

	got := ch.participantAPIURL("/events")
	want := "http://127.0.0.1:8080/v1/api/v1/channels/csgclaw/participants/test-bot/events?name=foo"
	if got != want {
		t.Fatalf("participantAPIURL() = %q, want %q", got, want)
	}
}
