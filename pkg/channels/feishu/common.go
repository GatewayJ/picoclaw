package feishu

import (
	"encoding/json"
	"regexp"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// mentionPlaceholderRegex matches @_user_N placeholders inserted by Feishu for mentions.
var mentionPlaceholderRegex = regexp.MustCompile(`@_user_\d+`)

// stringValue safely dereferences a *string pointer.
func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// buildMarkdownCard builds a Feishu Interactive Card JSON 2.0 string with markdown content.
// JSON 2.0 cards support full CommonMark standard markdown syntax.
func buildMarkdownCard(content string) (string, error) {
	card := map[string]any{
		"schema": "2.0",
		"body": map[string]any{
			"elements": []map[string]any{
				{
					"tag":     "markdown",
					"content": content,
				},
			},
		},
	}
	data, err := json.Marshal(card)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// extractJSONStringField unmarshals content as JSON and returns the value of the given string field.
// Returns "" if the content is invalid JSON or the field is missing/empty.
func extractJSONStringField(content, field string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &m); err != nil {
		return ""
	}
	raw, ok := m[field]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// extractImageKey extracts the image_key from a Feishu image message content JSON.
// Format: {"image_key": "img_xxx"}
func extractImageKey(content string) string { return extractJSONStringField(content, "image_key") }

// extractFileKey extracts the file_key from a Feishu file/audio message content JSON.
// Format: {"file_key": "file_xxx", "file_name": "...", ...}
func extractFileKey(content string) string { return extractJSONStringField(content, "file_key") }

// extractFileName extracts the file_name from a Feishu file message content JSON.
func extractFileName(content string) string { return extractJSONStringField(content, "file_name") }

// extractPostImageKeys extracts image_key values from Feishu rich text post content.
// Format: {"title":"","content":[[{"tag":"img","image_key":"img_xxx"}]]}
func extractPostImageKeys(content string) []string {
	var payload struct {
		Content [][]map[string]any `json:"content"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return nil
	}

	var keys []string
	for _, line := range payload.Content {
		for _, elem := range line {
			if tag, _ := elem["tag"].(string); tag != "img" {
				continue
			}
			if key, _ := elem["image_key"].(string); key != "" {
				keys = append(keys, key)
			}
		}
	}
	return keys
}

// extractPostText flattens Feishu rich text post content into plain text.
// It handles the small set of rich-text tags needed for inbound instructions;
// image materialization is handled separately.
func extractPostText(content string) string {
	var payload struct {
		Title   string             `json:"title"`
		Content [][]map[string]any `json:"content"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return ""
	}

	var lines []string
	if title := strings.TrimSpace(payload.Title); title != "" {
		lines = append(lines, title)
	}
	for _, line := range payload.Content {
		var b strings.Builder
		for _, elem := range line {
			switch tag, _ := elem["tag"].(string); tag {
			case "text", "a":
				b.WriteString(postStringField(elem, "text"))
			case "at":
				name := postStringField(elem, "user_name")
				if name == "" {
					name = postStringField(elem, "text")
				}
				if name != "" {
					b.WriteString("@")
					b.WriteString(name)
				}
			}
		}
		if text := strings.TrimSpace(b.String()); text != "" {
			lines = append(lines, text)
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func postStringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	value, _ := m[key].(string)
	return value
}

// stripMentionPlaceholders removes @_user_N placeholders from the text content.
// These are inserted by Feishu when users @mention someone in a message.
func stripMentionPlaceholders(content string, mentions []*larkim.MentionEvent) string {
	if len(mentions) == 0 {
		return content
	}
	for _, m := range mentions {
		if m.Key != nil && *m.Key != "" {
			content = strings.ReplaceAll(content, *m.Key, "")
		}
	}
	// Also clean up any remaining @_user_N patterns
	content = mentionPlaceholderRegex.ReplaceAllString(content, "")
	return strings.TrimSpace(content)
}

// extractCardImageKeys recursively extracts all image keys from a Feishu interactive card.
// Image keys are used to download images from Feishu API.
// Returns two slices: Feishu-hosted keys and external URLs.
func extractCardImageKeys(rawContent string) (feishuKeys []string, externalURLs []string) {
	if rawContent == "" {
		return nil, nil
	}

	var card map[string]any
	if err := json.Unmarshal([]byte(rawContent), &card); err != nil {
		return nil, nil
	}

	extractImageKeysRecursive(card, &feishuKeys, &externalURLs)
	return feishuKeys, externalURLs
}

// isExternalURL returns true if the string is an external HTTP/HTTPS URL.
func isExternalURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// extractImageKeysRecursive traverses card structure to find all image keys.
// Collects both Feishu-hosted keys and external URLs separately.
func extractImageKeysRecursive(v any, feishuKeys, externalURLs *[]string) {
	switch val := v.(type) {
	case map[string]any:
		// Check if this is an img element
		if tag, ok := val["tag"].(string); ok {
			switch tag {
			case "img":
				// Try img_key first (always Feishu-hosted)
				if imgKey, ok := val["img_key"].(string); ok && imgKey != "" {
					*feishuKeys = append(*feishuKeys, imgKey)
				}
				// Check src - could be Feishu key or external URL
				if src, ok := val["src"].(string); ok && src != "" {
					if isExternalURL(src) {
						*externalURLs = append(*externalURLs, src)
					} else {
						*feishuKeys = append(*feishuKeys, src)
					}
				}
			case "icon":
				// Icon elements use icon_key
				if iconKey, ok := val["icon_key"].(string); ok && iconKey != "" {
					*feishuKeys = append(*feishuKeys, iconKey)
				}
			}
		}
		// Recurse into all nested structures
		for _, child := range val {
			extractImageKeysRecursive(child, feishuKeys, externalURLs)
		}
	case []any:
		for _, item := range val {
			extractImageKeysRecursive(item, feishuKeys, externalURLs)
		}
	}
}
