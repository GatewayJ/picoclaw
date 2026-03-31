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
	"regexp"
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
	inboundMentionPrefixRe     = regexp.MustCompile(`^[＠@]([^\s:：,，]+)(?:[\s]+|[:：,，]\s*)`)
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
	MessageID string   `json:"message_id"`
	ChatID    string   `json:"chat_id"`
	ChatType  string   `json:"chat_type"`
	Sender    sender   `json:"sender"`
	Text      string   `json:"text"`
	Timestamp string   `json:"timestamp"`
	Mentions  []string `json:"mentions"`
}

type sender struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

type sendRequest struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
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
	if strings.TrimSpace(msg.ChatID) == "" {
		return fmt.Errorf("csgclaw chat ID is empty: %w", channels.ErrSendFailed)
	}
	if strings.TrimSpace(msg.Content) == "" {
		return fmt.Errorf("csgclaw content is empty: %w", channels.ErrSendFailed)
	}

	body, err := json.Marshal(sendRequest{
		ChatID: msg.ChatID,
		Text:   msg.Content,
	})
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
		"chat_id":      msg.ChatID,
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
		"chat_id":       msg.ChatID,
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
				"error":   err.Error(),
				"backoff": backoff.String(),
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
	if strings.TrimSpace(evt.ChatID) == "" || strings.TrimSpace(evt.Sender.ID) == "" {
		return
	}

	fmt.Printf("evt: %+v\n", evt)

	peerKind := "direct"
	content, ok := stripInboundMentionPrefix(strings.TrimSpace(evt.Text), c.config.BotID)
	if !ok {
		return
	}
	if strings.EqualFold(evt.ChatType, "group") {
		peerKind = "group"
		shouldRespond, normalized := c.ShouldRespondInGroup(true, content)
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
	}
	if len(evt.Mentions) > 0 {
		metadata["mentions"] = strings.Join(evt.Mentions, ",")
	}

	c.HandleMessage(
		c.ctx,
		bus.Peer{Kind: peerKind, ID: evt.ChatID},
		evt.MessageID,
		evt.Sender.ID,
		evt.ChatID,
		content,
		nil,
		metadata,
		senderInfo,
	)
}

func stripInboundMentionPrefix(content, botID string) (string, bool) {
	content = strings.TrimSpace(content)
	match := inboundMentionPrefixRe.FindStringSubmatch(content)
	if len(match) != 2 {
		return "", false
	}

	if !isInboundMentionForBot(match[1], botID) {
		return "", false
	}

	return strings.TrimSpace(inboundMentionPrefixRe.ReplaceAllString(content, "")), true
}

func isInboundMentionForBot(mentionName, botID string) bool {
	mentionName = normalizeInboundMentionName(mentionName)
	for _, candidate := range inboundBotMentionNames(botID) {
		if mentionName == candidate {
			return true
		}
	}
	return false
}

func inboundBotMentionNames(botID string) []string {
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return nil
	}

	names := []string{normalizeInboundMentionName(botID)}
	if rest, ok := strings.CutPrefix(botID, "u-"); ok && strings.TrimSpace(rest) != "" {
		names = append(names, normalizeInboundMentionName(rest))
	}
	if rest, ok := strings.CutPrefix(botID, "u_"); ok && strings.TrimSpace(rest) != "" {
		names = append(names, normalizeInboundMentionName(rest))
	}
	return names
}

func normalizeInboundMentionName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
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
	base := strings.TrimRight(c.config.BaseURL, "/")
	return fmt.Sprintf("%s/api/bots/%s%s", base, url.PathEscape(c.config.BotID), suffix)
}
