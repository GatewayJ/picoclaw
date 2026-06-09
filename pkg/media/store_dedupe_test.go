package media

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileMediaStoreStoreReusesRefForSameScopeAndPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attachment.txt")
	if err := os.WriteFile(path, []byte("attachment"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := NewFileMediaStore()
	firstStoredAt := time.Unix(100, 0)
	secondStoredAt := time.Unix(200, 0)
	store.nowFunc = func() time.Time { return firstStoredAt }

	ref1, err := store.Store(path, MediaMeta{Filename: "attachment.txt"}, "scope")
	if err != nil {
		t.Fatalf("Store() first error = %v", err)
	}
	store.nowFunc = func() time.Time { return secondStoredAt }

	ref2, err := store.Store(path, MediaMeta{Filename: "renamed.txt"}, "scope")
	if err != nil {
		t.Fatalf("Store() second error = %v", err)
	}
	if ref1 != ref2 {
		t.Fatalf("Store() refs = %q and %q, want same ref", ref1, ref2)
	}
	if got := store.refs[ref1].storedAt; !got.Equal(secondStoredAt) {
		t.Fatalf("storedAt = %v, want refreshed %v", got, secondStoredAt)
	}
	if got := store.refs[ref1].meta.Filename; got != "renamed.txt" {
		t.Fatalf("meta.Filename = %q, want updated metadata", got)
	}
}
