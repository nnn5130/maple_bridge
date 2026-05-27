package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/maple/maple_bridge/internal/claude"
	"github.com/maple/maple_bridge/internal/config"
)

type Client struct {
	cfg       *config.Config
	apiClient *lark.Client
	runner    *claude.Runner
}

func NewClient(cfg *config.Config) (*Client, error) {
	apiClient := lark.NewClient(cfg.Feishu.AppID, cfg.Feishu.AppSecret,
		lark.WithLogReqAtDebug(true),
		lark.WithLogLevel(larkcore.LogLevelDebug),
	)

	runner := claude.NewRunner(cfg.Claude.Path, cfg.WorkingDir, cfg.Session.MaxIdleMinutes)

	return &Client{
		cfg:       cfg,
		apiClient: apiClient,
		runner:    runner,
	}, nil
}

func (c *Client) Start(ctx context.Context) error {
	eventDispatcher := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			return c.handleMessage(ctx, event)
		})

	wsClient := larkws.NewClient(
		c.cfg.Feishu.AppID,
		c.cfg.Feishu.AppSecret,
		larkws.WithEventHandler(eventDispatcher),
		larkws.WithLogLevel(larkcore.LogLevelDebug),
	)

	slog.Info("connecting to feishu via websocket...")
	return wsClient.Start(ctx)
}

func (c *Client) handleMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if event.Event == nil {
		return nil
	}

	msg := event.Event.Message
	if msg == nil {
		return nil
	}

	sender := event.Event.Sender
	if sender == nil || sender.SenderId == nil || sender.SenderId.OpenId == nil {
		return nil
	}
	senderID := *sender.SenderId.OpenId

	senderType := ""
	if sender.SenderType != nil {
		senderType = *sender.SenderType
	}
	if senderType != "user" {
		return nil
	}

	if !c.isAllowed(senderID) {
		slog.Warn("unauthorized user", "sender_id", senderID)
		return nil
	}

	text := extractText(*msg.Content)
	if text == "" {
		return nil
	}

	chatID := *msg.ChatId

	// Special commands
	switch {
	case text == "/reset":
		c.runner.Reset(senderID)
		c.sendText(ctx, chatID, "Session reset.")
		return nil
	}

	slog.Info("processing message", "sender", senderID, "text", truncate(text, 100))

	go c.process(senderID, chatID, text)
	return nil
}

func (c *Client) process(userID, chatID, text string) {
	ctx := context.Background()

	result, err := c.runner.Run(ctx, userID, text)
	if err != nil {
		slog.Error("claude run failed", "error", err)
		c.sendText(ctx, chatID, fmt.Sprintf("Error: %s", err))
		return
	}

	output := result.Text
	if output == "" {
		output = "(no response)"
	}

	c.sendText(ctx, chatID, output)
	slog.Info("response sent", "user", userID, "cost", fmt.Sprintf("$%.4f", result.CostUSD))
}

func (c *Client) isAllowed(userID string) bool {
	if len(c.cfg.AllowedUsers) == 0 {
		return true
	}
	for _, u := range c.cfg.AllowedUsers {
		if u == userID {
			return true
		}
	}
	return false
}

func (c *Client) sendText(ctx context.Context, chatID, text string) {
	// Split long messages (Feishu has ~4000 char limit per message)
	for _, chunk := range splitMessage(text, 3500) {
		content, _ := json.Marshal(map[string]string{"text": chunk})
		_, err := c.apiClient.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
			ReceiveIdType("chat_id").
			Body(larkim.NewCreateMessageReqBodyBuilder().
				MsgType("text").
				ReceiveId(chatID).
				Content(string(content)).
				Build()).
			Build())
		if err != nil {
			slog.Error("send message", "error", err)
		}
	}
}

func extractText(content string) string {
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(content), &data); err != nil {
		return strings.TrimSpace(content)
	}
	if text, ok := data["text"].(string); ok {
		return strings.TrimSpace(text)
	}
	return content
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}
	var chunks []string
	for len(text) > maxLen {
		chunks = append(chunks, text[:maxLen])
		text = text[maxLen:]
	}
	if text != "" {
		chunks = append(chunks, text)
	}
	return chunks
}
