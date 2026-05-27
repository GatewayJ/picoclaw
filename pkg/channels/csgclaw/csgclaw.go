package csgclaw

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	defaultHTTPTimeout = 30 * time.Second
	sseReadTimeout     = 0
)

var (
	sseReconnectInitialBackoff = 1 * time.Second
	sseReconnectMaxBackoff     = 30 * time.Second
)

type Channel struct {
	*channels.BaseChannel
	config     config.CSGClawConfig
	httpClient *http.Client

	ctx    context.Context
	cancel context.CancelFunc

	respMu  sync.Mutex
	eventRC io.ReadCloser
}

type eventPayload struct {
	MessageID    string       `json:"message_id"`
	RoomID       string       `json:"room_id"`
	ChatID       string       `json:"chat_id"`
	ChatType     string       `json:"chat_type"`
	ThreadRootID string       `json:"thread_root_id"`
	Sender       sender       `json:"sender"`
	Text         string       `json:"text"`
	Timestamp    string       `json:"timestamp"`
	Mentions     []string     `json:"mentions"`
	Context      eventContext `json:"context"`
}

type eventContext struct {
	Channel  string `json:"channel"`
	ChatID   string `json:"chat_id"`
	ChatType string `json:"chat_type"`
	TopicID  string `json:"topic_id"`
}

type sender struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

type sendRequest struct {
	RoomID  string       `json:"room_id"`
	Text    string       `json:"text"`
	TopicID string       `json:"topic_id,omitempty"`
	Context *sendContext `json:"context,omitempty"`
}

type sendContext struct {
	Channel string `json:"channel,omitempty"`
	ChatID  string `json:"chat_id,omitempty"`
	TopicID string `json:"topic_id,omitempty"`
}

func NewChannel(cfg config.CSGClawConfig, messageBus *bus.MessageBus) (*Channel, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, fmt.Errorf("csgclaw base_url is required")
	}
	if strings.TrimSpace(cfg.BotID) == "" {
		return nil, fmt.Errorf("csgclaw bot_id is required")
	}
	if strings.TrimSpace(cfg.AccessToken) == "" {
		return nil, fmt.Errorf("csgclaw access_token is required")
	}

	base := channels.NewBaseChannel(
		"csgclaw",
		cfg,
		messageBus,
		cfg.AllowFrom,
		channels.WithGroupTrigger(cfg.GroupTrigger),
		channels.WithReasoningChannelID(cfg.ReasoningChannelID),
	)

	return &Channel{
		BaseChannel: base,
		config:      cfg,
		httpClient: &http.Client{
			Timeout: defaultHTTPTimeout,
		},
	}, nil
}

func (c *Channel) Start(ctx context.Context) error {
	logger.InfoC("csgclaw", "Starting CSGClaw channel")

	c.ctx, c.cancel = context.WithCancel(ctx)

	c.SetRunning(true)
	go c.runEventLoop()

	logger.InfoCF("csgclaw", "CSGClaw channel started", map[string]any{
		"base_url": c.config.BaseURL,
		"bot_id":   c.config.BotID,
	})
	return nil
}

func (c *Channel) Stop(ctx context.Context) error {
	logger.InfoC("csgclaw", "Stopping CSGClaw channel")

	c.SetRunning(false)

	if c.cancel != nil {
		c.cancel()
	}

	c.closeEventStream()
	return nil
}

func (c *Channel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return channels.ErrNotRunning
	}
	roomID, topicID := splitTopicChatID(msg.ChatID)
	if strings.TrimSpace(roomID) == "" {
		return fmt.Errorf("csgclaw chat ID is empty: %w", channels.ErrSendFailed)
	}
	if strings.TrimSpace(msg.Content) == "" {
		return fmt.Errorf("csgclaw content is empty: %w", channels.ErrSendFailed)
	}

	payload := sendRequest{
		RoomID: roomID,
		Text:   msg.Content,
	}
	if topicID != "" {
		payload.TopicID = topicID
		payload.Context = &sendContext{
			Channel: "csgclaw",
			ChatID:  roomID,
			TopicID: topicID,
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("csgclaw marshal send payload: %w", channels.ErrSendFailed)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.sendURL(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("csgclaw build send request: %w", channels.ErrSendFailed)
	}
	req.Header.Set("Authorization", "Bearer "+c.config.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	logger.InfoCF("csgclaw", "Sending outbound message", map[string]any{
		"room_id":      roomID,
		"topic_id":     topicID,
		"content_len":  len(msg.Content),
		"endpoint_url": c.sendURL(),
	})

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("csgclaw send request: %w", channels.ClassifyNetError(err))
	}
	defer resp.Body.Close()

	rawBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return channels.ClassifySendError(
			resp.StatusCode,
			fmt.Errorf("csgclaw send status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(rawBody))),
		)
	}

	logger.InfoCF("csgclaw", "Outbound message sent", map[string]any{
		"room_id":       roomID,
		"status_code":   resp.StatusCode,
		"response_body": strings.TrimSpace(string(rawBody)),
	})

	return nil
}

func (c *Channel) openEventStream(ctx context.Context) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.eventsURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("csgclaw build events request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.config.AccessToken)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	client := &http.Client{
		Timeout: sseReadTimeout,
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("csgclaw connect events stream: %w", channels.ClassifyNetError(err))
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		rawBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, channels.ClassifySendError(
			resp.StatusCode,
			fmt.Errorf("csgclaw events status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(rawBody))),
		)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		defer resp.Body.Close()
		return nil, fmt.Errorf("csgclaw events endpoint returned non-SSE content type %q", resp.Header.Get("Content-Type"))
	}
	return resp, nil
}

func (c *Channel) runEventLoop() {
	backoff := sseReconnectInitialBackoff

	for {
		if c.ctx.Err() != nil {
			return
		}

		resp, err := c.openEventStream(c.ctx)
		if err != nil {
			if c.ctx.Err() != nil {
				return
			}

			logger.WarnCF("csgclaw", "Failed to connect SSE stream, will retry", map[string]any{
				"error":      err.Error(),
				"backoff":    backoff.String(),
				"events_url": c.eventsURL(),
			})
			if !sleepWithContext(c.ctx, backoff) {
				return
			}
			backoff = minDuration(backoff*2, sseReconnectMaxBackoff)
			continue
		}

		c.setEventStream(resp.Body)
		logger.InfoCF("csgclaw", "CSGClaw SSE stream connected", map[string]any{
			"events_url": c.eventsURL(),
		})

		err = c.consumeEvents(resp)
		if c.ctx.Err() != nil {
			return
		}

		logger.WarnCF("csgclaw", "CSGClaw SSE stream disconnected, reconnecting", map[string]any{
			"error":   err.Error(),
			"backoff": backoff.String(),
		})
		if !sleepWithContext(c.ctx, backoff) {
			return
		}
		backoff = minDuration(backoff*2, sseReconnectMaxBackoff)
	}
}

func (c *Channel) consumeEvents(resp *http.Response) error {
	defer func() {
		_ = resp.Body.Close()
		c.clearEventStream(resp.Body)
	}()

	reader := bufio.NewReader(resp.Body)
	var (
		eventType string
		dataLines []string
	)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if c.ctx.Err() != nil {
				return nil
			}
			return err
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			c.dispatchEvent(eventType, strings.Join(dataLines, "\n"))
			eventType = ""
			dataLines = dataLines[:0]
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
}

func (c *Channel) dispatchEvent(eventType, raw string) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	if eventType != "" && eventType != "message" {
		return
	}

	var evt eventPayload
	if err := json.Unmarshal([]byte(raw), &evt); err != nil {
		logger.ErrorCF("csgclaw", "Failed to decode message event", map[string]any{
			"error": err.Error(),
			"event": raw,
		})
		return
	}

	c.handleInboundEvent(evt)
}

func (c *Channel) handleInboundEvent(evt eventPayload) {
	logger.DebugCF("csgclaw", "Received inbound event", map[string]any{
		"event": evt,
	})

	roomID := resolvedRoomID(evt)
	topicID := resolvedTopicID(evt)
	chatID := topicChatID(roomID, topicID)

	if strings.TrimSpace(roomID) == "" || strings.TrimSpace(evt.Sender.ID) == "" {
		return
	}

	peerKind := "direct"
	content := strings.TrimSpace(evt.Text)
	if strings.EqualFold(evt.ChatType, "group") {
		peerKind = "group"
		isMentioned := hasInboundBotAtMention(content, c.config.BotID)
		if !isMentioned && hasInboundAtMention(content) {
			return
		}
		content = normalizeInboundAtMentions(content)
		shouldRespond, normalized := c.ShouldRespondInGroup(isMentioned, content)
		if !shouldRespond {
			return
		}
		content = normalized
	}

	senderInfo := bus.SenderInfo{
		Platform:    "csgclaw",
		PlatformID:  evt.Sender.ID,
		CanonicalID: identity.BuildCanonicalID("csgclaw", evt.Sender.ID),
		Username:    evt.Sender.Username,
		DisplayName: evt.Sender.DisplayName,
	}

	metadata := map[string]string{
		"timestamp": evt.Timestamp,
		"chat_type": evt.ChatType,
		"room_id":   roomID,
	}
	if len(evt.Mentions) > 0 {
		metadata["mentions"] = strings.Join(evt.Mentions, ",")
	}
	if topicID != "" {
		metadata["topic_id"] = topicID
		metadata["parent_peer_kind"] = "topic"
		metadata["parent_peer_id"] = topicID
	}
	if strings.TrimSpace(evt.ThreadRootID) != "" {
		metadata["thread_root_id"] = strings.TrimSpace(evt.ThreadRootID)
	}

	c.HandleMessage(
		c.ctx,
		bus.Peer{Kind: peerKind, ID: chatID},
		evt.MessageID,
		evt.Sender.ID,
		chatID,
		content,
		nil,
		metadata,
		senderInfo,
	)
}

func resolvedRoomID(evt eventPayload) string {
	if roomID := strings.TrimSpace(evt.RoomID); roomID != "" {
		return roomID
	}
	if chatID := strings.TrimSpace(evt.ChatID); chatID != "" {
		return chatID
	}
	return strings.TrimSpace(evt.Context.ChatID)
}

func resolvedTopicID(evt eventPayload) string {
	if topicID := strings.TrimSpace(evt.Context.TopicID); topicID != "" {
		return topicID
	}
	return strings.TrimSpace(evt.ThreadRootID)
}

func topicChatID(roomID, topicID string) string {
	roomID = strings.TrimSpace(roomID)
	topicID = strings.TrimSpace(topicID)
	if roomID == "" || topicID == "" {
		return roomID
	}
	return roomID + "/" + topicID
}

func splitTopicChatID(chatID string) (roomID, topicID string) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return "", ""
	}
	idx := strings.LastIndex(chatID, "/")
	if idx <= 0 || idx == len(chatID)-1 {
		return chatID, ""
	}
	return strings.TrimSpace(chatID[:idx]), strings.TrimSpace(chatID[idx+1:])
}

func hasInboundBotAtMention(content, botID string) bool {
	content = strings.TrimSpace(content)
	botID = strings.TrimSpace(botID)
	if content == "" || botID == "" {
		return false
	}

	const prefix = `<at user_id="`
	searchFrom := 0
	for {
		start := strings.Index(content[searchFrom:], prefix)
		if start < 0 {
			return false
		}
		start += searchFrom + len(prefix)
		end := strings.IndexByte(content[start:], '"')
		if end < 0 {
			return false
		}
		if strings.TrimSpace(content[start:start+end]) == botID {
			return true
		}
		searchFrom = start + end + 1
	}
}

func hasInboundAtMention(content string) bool {
	content = strings.TrimSpace(content)
	if content == "" {
		return false
	}
	return strings.Contains(content, `<at user_id="`)
}

func normalizeInboundAtMentions(content string) string {
	if content == "" {
		return content
	}

	const (
		openTag  = "<at"
		closeTag = "</at>"
	)

	var builder strings.Builder
	builder.Grow(len(content))

	searchFrom := 0
	for {
		start := strings.Index(content[searchFrom:], openTag)
		if start < 0 {
			builder.WriteString(content[searchFrom:])
			return builder.String()
		}
		start += searchFrom
		builder.WriteString(content[searchFrom:start])

		tagEnd := strings.IndexByte(content[start:], '>')
		if tagEnd < 0 {
			builder.WriteString(content[start:])
			return builder.String()
		}
		tagEnd += start

		closeStart := strings.Index(content[tagEnd+1:], closeTag)
		if closeStart < 0 {
			builder.WriteString(content[start:])
			return builder.String()
		}
		closeStart += tagEnd + 1

		mentionName := strings.TrimSpace(content[tagEnd+1 : closeStart])
		if mentionName == "" {
			builder.WriteString(content[start : closeStart+len(closeTag)])
		} else {
			builder.WriteByte('@')
			builder.WriteString(mentionName)
		}

		searchFrom = closeStart + len(closeTag)
	}
}

func (c *Channel) closeEventStream() {
	c.respMu.Lock()
	defer c.respMu.Unlock()
	if c.eventRC != nil {
		_ = c.eventRC.Close()
		c.eventRC = nil
	}
}

func (c *Channel) setEventStream(rc io.ReadCloser) {
	c.respMu.Lock()
	defer c.respMu.Unlock()
	c.eventRC = rc
}

func (c *Channel) clearEventStream(rc io.ReadCloser) {
	c.respMu.Lock()
	defer c.respMu.Unlock()
	if c.eventRC == rc {
		c.eventRC = nil
	}
}

func sleepWithContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func (c *Channel) eventsURL() string {
	return c.botAPIURL("/events")
}

func (c *Channel) sendURL() string {
	return c.botAPIURL("/messages/send")
}

func (c *Channel) botAPIURL(suffix string) string {
	baseURL, err := url.Parse(c.config.BaseURL)
	if err != nil {
		base := strings.TrimRight(c.config.BaseURL, "/")
		return fmt.Sprintf("%s/api/bots/%s%s", base, url.PathEscape(c.config.BotID), suffix)
	}

	pathParts := []string{"api", "bots", c.config.BotID}
	for _, part := range strings.Split(strings.Trim(suffix, "/"), "/") {
		if part == "" {
			continue
		}
		pathParts = append(pathParts, part)
	}

	basePath := baseURL.EscapedPath()
	if basePath == "" {
		basePath = "/"
	}
	baseURL.Path = path.Join(append([]string{basePath}, pathParts...)...)
	baseURL.RawPath = ""
	return baseURL.String()
}
