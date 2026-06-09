//go:build amd64 || arm64 || riscv64 || mips64 || ppc64

package feishu

import (
	"reflect"
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestExtractPostTextAndImageKeys(t *testing.T) {
	raw := `{"title":"","content":[[{"tag":"img","image_key":"img_1"}],[{"tag":"text","text":"\u5c06\u8fd9\u4e2a\u56fe\u7247\u8bc4\u8bae\u5728 issue \u4e2d"}],[{"tag":"img","image_key":"img_2"}]]}`

	if got, want := extractPostText(raw), "\u5c06\u8fd9\u4e2a\u56fe\u7247\u8bc4\u8bae\u5728 issue \u4e2d"; got != want {
		t.Fatalf("extractPostText() = %q, want %q", got, want)
	}
	if got, want := extractPostImageKeys(raw), []string{"img_1", "img_2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("extractPostImageKeys() = %#v, want %#v", got, want)
	}
}

func TestAppendMediaTagsPostUsesImageTag(t *testing.T) {
	got := appendMediaTags(
		"\u5c06\u8fd9\u4e2a\u56fe\u7247\u8bc4\u8bae\u5728 issue \u4e2d",
		larkim.MsgTypePost,
		[]string{"media://img"},
	)
	want := "\u5c06\u8fd9\u4e2a\u56fe\u7247\u8bc4\u8bae\u5728 issue \u4e2d [image: photo]"
	if got != want {
		t.Fatalf("appendMediaTags(post) = %q, want %q", got, want)
	}
}

func TestAppendAttachmentFailures(t *testing.T) {
	got := appendAttachmentFailures("hello", []string{"image img_1: unavailable (feishu resource API code=999)"})
	want := "hello\n\nAttachments:\n- image img_1: unavailable (feishu resource API code=999)"
	if got != want {
		t.Fatalf("appendAttachmentFailures() = %q, want %q", got, want)
	}
}
