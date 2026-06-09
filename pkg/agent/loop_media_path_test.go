package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestResolveMediaRefsImageAddsDataURLAndPathTag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.png")
	if err := os.WriteFile(path, []byte("not a real png, but metadata supplies MIME"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := media.NewFileMediaStore()
	ref, err := store.Store(path, media.MediaMeta{ContentType: "image/png"}, "scope")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	result := resolveMediaRefs([]providers.Message{{
		Role:    "user",
		Content: "[image: photo]",
		Media:   []string{ref},
	}}, store, 1024*1024)

	if len(result) != 1 {
		t.Fatalf("result len = %d, want 1", len(result))
	}
	if len(result[0].Media) != 1 || !strings.HasPrefix(result[0].Media[0], "data:image/png;base64,") {
		t.Fatalf("resolved media = %#v, want data:image/png", result[0].Media)
	}
	if want := "[image:" + path + "]"; !strings.Contains(result[0].Content, want) {
		t.Fatalf("content = %q, want path tag %q", result[0].Content, want)
	}
	if strings.Contains(result[0].Content, "[image: photo]") {
		t.Fatalf("content still contains generic image tag: %q", result[0].Content)
	}
}

func TestResolvedCurrentUserMessageContentReturnsResolvedLastUser(t *testing.T) {
	messages := []providers.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "old"},
		{Role: "assistant", Content: "answer"},
		{Role: "user", Content: "new [image:/tmp/picoclaw_media/a.jpg]"},
	}

	got := resolvedCurrentUserMessageContent(messages, "fallback")
	want := "new [image:/tmp/picoclaw_media/a.jpg]"
	if got != want {
		t.Fatalf("resolvedCurrentUserMessageContent() = %q, want %q", got, want)
	}
}

func TestResolvedImageMessageContentCanRemainInHistoryForFollowupText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(path, []byte("jpeg bytes supplied by Feishu"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := media.NewFileMediaStore()
	ref, err := store.Store(path, media.MediaMeta{ContentType: "image/jpeg"}, "scope")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	resolved := resolveMediaRefs([]providers.Message{{
		Role:    "user",
		Content: "[image: photo]",
		Media:   []string{ref},
	}}, store, 1024*1024)
	historyContent := resolvedCurrentUserMessageContent(resolved, "[image: photo]")
	wantPathTag := "[image:" + path + "]"
	if !strings.Contains(historyContent, wantPathTag) {
		t.Fatalf("history content = %q, want path tag %q", historyContent, wantPathTag)
	}

	followupMessages := []providers.Message{
		{Role: "user", Content: historyContent},
		{Role: "user", Content: "\u628a\u8fd9\u4e2a\u56fe\u7247\u8bc4\u8bae\u5230 issue"},
	}
	if !strings.Contains(followupMessages[0].Content, wantPathTag) {
		t.Fatalf("follow-up history lost path tag: %#v", followupMessages)
	}
}
