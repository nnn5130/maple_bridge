package feishu

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/nnn5130/maple_bridge/internal/codex"
	"github.com/nnn5130/maple_bridge/internal/config"
)

type Client struct {
	cfgPath   string
	cfgMu     sync.RWMutex
	cfg       *config.Config
	apiClient *lark.Client
	runnerMu  sync.RWMutex
	runner    *codex.Runner
	rootCtxMu sync.RWMutex
	rootCtx   context.Context
	procMu    sync.Mutex
	procs     map[int]*managedProcess
	procStore string
	seenMu    sync.Mutex
	seenMsgs  map[string]time.Time
}

const (
	messageDedupeTTL   = 10 * time.Minute
	commandOutputLimit = 4000
	cardPreviewLimit   = 1200
	stopWaitTimeout    = 3 * time.Second
	stopPollInterval   = 100 * time.Millisecond
)

type finalDelivery string

const (
	finalDeliveryCard  finalDelivery = "card"
	finalDeliveryText  finalDelivery = "text"
	finalDeliveryReply finalDelivery = "reply"
)

type managedProcess struct {
	PID         int
	OwnerUserID string
	OwnerName   string
	Command     string
	WorkDir     string
	LogPath     string
	StartedAt   time.Time
}

type sessionRef struct {
	Key        string
	Persistent bool
	Label      string
}

func NewClient(cfgPath string, cfg *config.Config) (*Client, error) {
	apiOptions := []lark.ClientOptionFunc{
		lark.WithLogLevel(larkLogLevel(cfg.LogLevel)),
	}
	if isDebugLogLevel(cfg.LogLevel) {
		apiOptions = append(apiOptions, lark.WithLogReqAtDebug(true))
	}
	apiClient := lark.NewClient(cfg.Feishu.AppID, cfg.Feishu.AppSecret, apiOptions...)

	runner := codex.NewRunner(cfg.Codex.Path, cfg.WorkingDir, cfg.Session.MaxIdleMinutes)

	client := &Client{
		cfgPath:   cfgPath,
		cfg:       cfg,
		apiClient: apiClient,
		runner:    runner,
		procs:     make(map[int]*managedProcess),
		procStore: filepath.Join(cfg.WorkingDir, ".maple_bridge", "managed_services.json"),
		seenMsgs:  make(map[string]time.Time),
	}
	client.restoreManagedServices()
	return client, nil
}

func (c *Client) Start(ctx context.Context) error {
	c.setRootContext(ctx)
	cfg := c.currentConfig()
	eventDispatcher := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			return c.handleMessage(ctx, event)
		})

	wsClient := larkws.NewClient(
		cfg.Feishu.AppID,
		cfg.Feishu.AppSecret,
		larkws.WithEventHandler(eventDispatcher),
		larkws.WithLogLevel(larkLogLevel(cfg.LogLevel)),
	)

	slog.Info("connecting to feishu via websocket...")
	return wsClient.Start(ctx)
}

func (c *Client) Close() {
	if runner := c.currentRunner(); runner != nil {
		runner.Close()
	}
}

func (c *Client) markMessageSeen(messageID string) bool {
	if messageID == "" {
		return true
	}
	now := time.Now()
	c.seenMu.Lock()
	defer c.seenMu.Unlock()
	if c.seenMsgs == nil {
		c.seenMsgs = make(map[string]time.Time)
	}
	for id, seenAt := range c.seenMsgs {
		if now.Sub(seenAt) > messageDedupeTTL {
			delete(c.seenMsgs, id)
		}
	}
	if _, ok := c.seenMsgs[messageID]; ok {
		return false
	}
	c.seenMsgs[messageID] = now
	return true
}

func (c *Client) handleMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if event.Event == nil {
		return nil
	}

	msg := event.Event.Message
	if msg == nil {
		return nil
	}
	if msg.Content == nil || msg.ChatId == nil || msg.MessageId == nil {
		slog.Warn("message missing required fields")
		return nil
	}

	sender := event.Event.Sender
	if sender == nil || sender.SenderId == nil || sender.SenderId.OpenId == nil {
		return nil
	}
	senderID := *sender.SenderId.OpenId
	cfg := c.currentConfig()

	senderType := ""
	if sender.SenderType != nil {
		senderType = *sender.SenderType
	}
	if senderType != "user" {
		return nil
	}

	if !c.isAllowed(cfg, senderID) {
		slog.Warn("unauthorized user", "sender_id", senderID)
		return nil
	}

	isAdmin := c.isAdmin(cfg, senderID)
	isSuperAdmin := c.isSuperAdmin(cfg, senderID)

	// In group chats, only respond when bot is mentioned (@bot)
	chatType := ""
	if msg.ChatType != nil {
		chatType = *msg.ChatType
	}
	if chatType == "group" && !c.isBotMentioned(msg) {
		return nil
	}

	text := extractText(*msg.Content)
	if text == "" {
		return nil
	}

	chatID := *msg.ChatId
	messageID := *msg.MessageId
	session := messageSession(msg)
	if !c.markMessageSeen(messageID) {
		slog.Info("duplicate message ignored", "message_id", messageID, "chat_id", chatID)
		return nil
	}

	// Built-in commands (no AI needed)
	switch {
	case text == "/help":
		c.sendText(ctx, chatID, helpText())
		return nil
	case text == "/reload":
		if !isSuperAdmin {
			c.sendText(ctx, chatID, "Only super-admin users can reload config.")
			return nil
		}
		c.sendText(ctx, chatID, c.reloadConfig())
		return nil
	case text == "/reset":
		c.currentRunner().Reset(session.Key, session.Persistent)
		c.sendText(ctx, chatID, "Session reset.")
		return nil
	case text == "/ll":
		c.sendText(ctx, chatID, c.listWorkDir(session, isSuperAdmin))
		return nil
	case text == "/workspace":
		c.sendText(ctx, chatID, c.listDir(cfg.WorkingDir))
		return nil
	case strings.HasPrefix(text, "/workspace "):
		c.sendText(ctx, chatID, "Deprecated: use /cd <dir> to switch directory.")
		return nil
	case strings.HasPrefix(text, "/cd "):
		dir := strings.TrimSpace(strings.TrimPrefix(text, "/cd "))
		c.sendText(ctx, chatID, c.changeWorkDir(session, dir, isSuperAdmin))
		return nil
	case text == "/status":
		if err := c.ensureWorkDirAllowed(session, isSuperAdmin); err != nil {
			c.sendText(ctx, chatID, err.Error())
			return nil
		}
		sessionID, wd, turns := c.currentRunner().SessionInfo(session.Key, session.Persistent)
		c.sendText(ctx, chatID, fmt.Sprintf("Session: %s\nScope: %s\nTurns: %d\nWorking dir: %s", sessionID, session.Label, turns, wd))
		return nil
	case text == "/model":
		c.sendText(ctx, chatID, fmt.Sprintf("Codex CLI: %s", cfg.Codex.Path))
		return nil
	case strings.HasPrefix(text, "/run "):
		if !isAdmin {
			c.sendText(ctx, chatID, "Only admin users can run shell commands.")
			return nil
		}
		cmd := strings.TrimSpace(strings.TrimPrefix(text, "/run "))
		go c.execCommand(session, senderID, chatID, cmd, isSuperAdmin)
		return nil
	case strings.HasPrefix(text, "/start "):
		if !isAdmin {
			c.sendText(ctx, chatID, "Only admin users can start background services.")
			return nil
		}
		command := strings.TrimSpace(strings.TrimPrefix(text, "/start "))
		go c.startService(session, senderID, chatID, command, isSuperAdmin)
		return nil
	case text == "/services":
		c.sendText(ctx, chatID, c.listServices(senderID, isSuperAdmin))
		return nil
	case text == "/pid":
		c.sendText(ctx, chatID, c.listServicePIDs(senderID, isSuperAdmin))
		return nil
	case strings.HasPrefix(text, "/stop "):
		if !isAdmin {
			c.sendText(ctx, chatID, "Only admin users can stop background services.")
			return nil
		}
		pidText := strings.TrimSpace(strings.TrimPrefix(text, "/stop "))
		c.sendText(ctx, chatID, c.stopService(senderID, isSuperAdmin, pidText))
		return nil
	case strings.HasPrefix(text, "/logs "):
		pidText := strings.TrimSpace(strings.TrimPrefix(text, "/logs "))
		c.sendText(ctx, chatID, c.serviceLogs(senderID, isSuperAdmin, pidText))
		return nil
	}

	slog.Info("processing message", "sender", senderID, "chat_id", chatID, "message_id", messageID, "text", truncate(redactSensitive(text), 100))

	go c.process(session, senderID, chatID, messageID, chatType, text, isAdmin, isSuperAdmin)
	return nil
}

func (c *Client) process(session sessionRef, userID, chatID, messageID, chatType, text string, isAdmin, isSuperAdmin bool) {
	ctx := c.backgroundContext()
	startedAt := time.Now()
	cardMessageID := c.sendStatusCard(ctx, chatID, "Codex processing", "正在处理请求...", text)

	if err := c.ensureWorkDirAllowed(session, isSuperAdmin); err != nil {
		msg := fmt.Sprintf("Error: %s", err)
		c.finishResponse(ctx, chatID, messageID, cardMessageID, chatType, "Codex failed", msg, text, startedAt)
		return
	}

	// Prefix message with Feishu context so Codex knows the origin.
	prefixed := fmt.Sprintf("[feishu chat_id=%s message_id=%s sender=%s sender_name=%s session=%s]\n%s", chatID, messageID, userID, c.displayName(c.currentConfig(), userID), session.Label, text)

	result, err := c.currentRunner().Run(ctx, session.Key, session.Persistent, prefixed, isAdmin)
	if err != nil {
		msg := userFacingError(err)
		slog.Error("codex run failed", "error", err, "user_error", msg)
		c.finishResponse(ctx, chatID, messageID, cardMessageID, chatType, "Codex failed", msg, text, startedAt)
		return
	}

	output := result.Text
	if output == "" {
		output = "(no response)"
	}

	c.finishResponse(ctx, chatID, messageID, cardMessageID, chatType, "Codex finished", output, text, startedAt)
	slog.Info("response sent", "user", userID)
}

func (c *Client) finishResponse(ctx context.Context, chatID, originalMessageID, cardMessageID, chatType, title, output, request string, startedAt time.Time) {
	delivery := finalDeliveryFor(chatType, output)
	duration := time.Since(startedAt).Round(time.Second)
	cardBody := finalCardBody(delivery, title, output, duration)

	cardPatched := false
	if cardMessageID != "" {
		cardPatched = c.patchStatusCard(ctx, cardMessageID, title, cardBody, request)
	}
	if !cardPatched && delivery != finalDeliveryCard {
		c.sendText(ctx, chatID, cardBody)
	}

	switch delivery {
	case finalDeliveryCard:
		if !cardPatched {
			c.sendText(ctx, chatID, output)
		}
	case finalDeliveryReply:
		c.sendReplyText(ctx, originalMessageID, chatID, output)
	case finalDeliveryText:
		c.sendText(ctx, chatID, output)
	}

	slog.Info("final response delivered", "chat_type", chatType, "delivery", delivery, "output_len", len(output), "card_patched", cardPatched, "duration", duration.String())
}

func finalDeliveryFor(chatType, output string) finalDelivery {
	if chatType == "group" {
		return finalDeliveryReply
	}
	if len(output) > cardPreviewLimit {
		return finalDeliveryText
	}
	return finalDeliveryCard
}

func finalCardBody(delivery finalDelivery, title, output string, duration time.Duration) string {
	status := "处理完成"
	if strings.Contains(strings.ToLower(title), "failed") {
		status = "处理失败"
	}
	switch delivery {
	case finalDeliveryReply:
		return fmt.Sprintf("%s\n耗时：%s\n结果已在下方回复。", status, duration)
	case finalDeliveryText:
		return fmt.Sprintf("%s\n耗时：%s\n结果较长，已拆分为下方消息。\n\n预览：\n%s", status, duration, truncate(output, cardPreviewLimit))
	default:
		return output
	}
}

func messageSession(msg *larkim.EventMessage) sessionRef {
	if msg == nil {
		return sessionRef{Key: "chat:unknown", Label: "chat unknown"}
	}
	chatID := strings.TrimSpace(stringValue(msg.ChatId))
	if chatID == "" {
		chatID = "unknown"
	}
	rootID := strings.TrimSpace(stringValue(msg.RootId))
	if rootID == "" {
		rootID = strings.TrimSpace(stringValue(msg.ParentId))
	}
	if rootID != "" {
		return sessionRef{
			Key:        "topic:" + chatID + ":" + rootID,
			Persistent: true,
			Label:      "topic " + rootID,
		}
	}
	return sessionRef{
		Key:   "chat:" + chatID,
		Label: "chat " + chatID,
	}
}

func stringValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func userFacingError(err error) string {
	if err == nil {
		return "Error: unknown error"
	}
	text := err.Error()
	for _, marker := range []string{"\nstderr:", "\nstdout:"} {
		if idx := strings.Index(text, marker); idx >= 0 {
			text = text[:idx]
		}
	}
	return "Error: " + strings.TrimSpace(text)
}

func redactSensitive(text string) string {
	return secretTokenRegex.ReplaceAllString(text, "[REDACTED]")
}

func (c *Client) execCommand(session sessionRef, userID, chatID, command string, isSuperAdmin bool) {
	ctx, cancel := context.WithTimeout(c.backgroundContext(), 30*time.Second)
	defer cancel()

	if err := c.ensureWorkDirAllowed(session, isSuperAdmin); err != nil {
		c.sendText(c.backgroundContext(), chatID, fmt.Sprintf("Error: %s", err))
		return
	}

	wd := c.currentRunner().GetWorkDir(session.Key, session.Persistent)
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = wd

	stdout := newLimitedBuffer(commandOutputLimit)
	stderr := newLimitedBuffer(commandOutputLimit)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\n[stderr]\n" + stderr.String()
	}
	if err != nil {
		output += fmt.Sprintf("\n[error: %s]", err)
	}
	if len(output) > 4000 {
		output = truncateWithNotice(output, commandOutputLimit)
	}
	if output == "" {
		output = "(no output)"
	}

	c.sendText(c.backgroundContext(), chatID, output)
	slog.Info("exec command", "user", userID, "command", command)
}

func (c *Client) listWorkDir(session sessionRef, isSuperAdmin bool) string {
	if err := c.ensureWorkDirAllowed(session, isSuperAdmin); err != nil {
		return err.Error()
	}
	wd := c.currentRunner().GetWorkDir(session.Key, session.Persistent)
	return c.listDir(wd)
}

func (c *Client) listDir(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Sprintf("Directory: %s\n(error reading: %s)", dir, err)
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("📁 %s", dir))
	for _, e := range entries {
		if e.IsDir() {
			lines = append(lines, fmt.Sprintf("  📁 %s/", e.Name()))
		} else {
			lines = append(lines, fmt.Sprintf("  📄 %s", e.Name()))
		}
	}
	return strings.Join(lines, "\n")
}

func (c *Client) changeWorkDir(session sessionRef, dir string, isSuperAdmin bool) string {
	if dir == "" {
		return "Usage: /cd <dir>"
	}

	runner := c.currentRunner()
	base := runner.GetWorkDir(session.Key, session.Persistent)
	target := dir
	if !filepath.IsAbs(target) {
		target = filepath.Join(base, target)
	}

	realTarget, err := resolveExistingDir(target)
	if err != nil {
		return fmt.Sprintf("Invalid directory: %s", err)
	}

	if !isSuperAdmin {
		realWorkspace, err := resolveExistingDir(c.currentConfig().WorkingDir)
		if err != nil {
			return fmt.Sprintf("Workspace is invalid: %s", err)
		}
		if !isPathWithin(realWorkspace, realTarget) {
			return fmt.Sprintf("Permission denied: admin and regular users are limited to workspace: %s", realWorkspace)
		}
	}

	runner.SetWorkDir(session.Key, session.Persistent, realTarget)
	return fmt.Sprintf("Working directory changed to: %s", realTarget)
}

func (c *Client) startService(session sessionRef, userID, chatID, command string, isSuperAdmin bool) {
	ctx := c.backgroundContext()
	if command == "" {
		c.sendText(ctx, chatID, "Usage: /start <command>")
		return
	}

	if err := c.ensureWorkDirAllowed(session, isSuperAdmin); err != nil {
		c.sendText(ctx, chatID, fmt.Sprintf("Error: %s", err))
		return
	}

	wd := c.currentRunner().GetWorkDir(session.Key, session.Persistent)
	logDir := filepath.Join(wd, ".maple_bridge", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		c.sendText(ctx, chatID, fmt.Sprintf("Create log dir failed: %s", err))
		return
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("service-%d.log", time.Now().UnixNano()))
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		c.sendText(ctx, chatID, fmt.Sprintf("Create log file failed: %s", err))
		return
	}

	cmd := exec.Command("bash", "-lc", command)
	cmd.Dir = wd
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		c.sendText(ctx, chatID, fmt.Sprintf("Start failed: %s", err))
		return
	}

	proc := &managedProcess{
		PID:         cmd.Process.Pid,
		OwnerUserID: userID,
		OwnerName:   c.displayName(c.currentConfig(), userID),
		Command:     command,
		WorkDir:     wd,
		LogPath:     logPath,
		StartedAt:   time.Now(),
	}
	c.procMu.Lock()
	c.procs[proc.PID] = proc
	c.saveManagedServicesLocked()
	c.procMu.Unlock()

	go func() {
		err := cmd.Wait()
		_ = logFile.Close()
		c.procMu.Lock()
		delete(c.procs, proc.PID)
		c.saveManagedServicesLocked()
		c.procMu.Unlock()
		slog.Info("managed service exited", "pid", proc.PID, "command", command, "error", err)
	}()

	c.sendText(ctx, chatID, fmt.Sprintf("Started service.\nPID: %d\nOwner: %s (%s)\nWork dir: %s\nLog: %s\nCommand: %s", proc.PID, proc.OwnerName, proc.OwnerUserID, proc.WorkDir, proc.LogPath, proc.Command))
}

func (c *Client) listServices(userID string, isSuperAdmin bool) string {
	c.procMu.Lock()
	defer c.procMu.Unlock()
	c.pruneManagedServicesLocked()

	var lines []string
	lines = append(lines, "Managed services:")
	for _, proc := range c.procs {
		if !canAccessProcess(userID, isSuperAdmin, proc) {
			continue
		}
		lines = append(lines, fmt.Sprintf("PID %d | %s | %s\n  owner: %s (%s)\n  cwd: %s\n  log: %s", proc.PID, time.Since(proc.StartedAt).Round(time.Second), proc.Command, proc.OwnerName, proc.OwnerUserID, proc.WorkDir, proc.LogPath))
	}
	if len(lines) == 1 {
		if isSuperAdmin {
			return "No managed services."
		}
		return "No managed services owned by you."
	}
	return strings.Join(lines, "\n")
}

func (c *Client) listServicePIDs(userID string, isSuperAdmin bool) string {
	c.procMu.Lock()
	defer c.procMu.Unlock()
	c.pruneManagedServicesLocked()

	var lines []string
	lines = append(lines, "Managed service PIDs:")
	for _, proc := range c.procs {
		if !canAccessProcess(userID, isSuperAdmin, proc) {
			continue
		}
		lines = append(lines, fmt.Sprintf("%d | owner=%s (%s) | %s", proc.PID, proc.OwnerName, proc.OwnerUserID, proc.Command))
	}
	if len(lines) == 1 {
		if isSuperAdmin {
			return "No managed service PIDs."
		}
		return "No managed service PIDs owned by you."
	}
	return strings.Join(lines, "\n")
}

func (c *Client) stopService(userID string, isSuperAdmin bool, pidText string) string {
	var pid int
	if _, err := fmt.Sscanf(pidText, "%d", &pid); err != nil || pid <= 0 {
		return "Usage: /stop <pid>"
	}

	c.procMu.Lock()
	proc, ok := c.procs[pid]
	c.procMu.Unlock()
	if !ok {
		return fmt.Sprintf("Managed service not found: %d", pid)
	}
	if !managedProcessAlive(proc) {
		c.procMu.Lock()
		delete(c.procs, pid)
		c.saveManagedServicesLocked()
		c.procMu.Unlock()
		return fmt.Sprintf("Managed service is no longer running: %d", pid)
	}
	if !canAccessProcess(userID, isSuperAdmin, proc) {
		return fmt.Sprintf("Permission denied: PID %d was started by %s (%s)", pid, proc.OwnerName, proc.OwnerUserID)
	}

	if err := signalManagedProcess(pid, syscall.SIGTERM); err != nil {
		return fmt.Sprintf("Stop failed for PID %d: %s", pid, err)
	}
	deadline := time.Now().Add(stopWaitTimeout)
	for time.Now().Before(deadline) {
		if !managedProcessAlive(proc) {
			c.procMu.Lock()
			delete(c.procs, pid)
			c.saveManagedServicesLocked()
			c.procMu.Unlock()
			return fmt.Sprintf("Stopped PID %d: %s", pid, proc.Command)
		}
		time.Sleep(stopPollInterval)
	}
	return fmt.Sprintf("Stop signal sent to PID %d, but it is still running after %s; keeping it managed: %s", pid, stopWaitTimeout, proc.Command)
}

func canAccessProcess(userID string, isSuperAdmin bool, proc *managedProcess) bool {
	return isSuperAdmin || proc.OwnerUserID == userID
}

func signalManagedProcess(pid int, signal syscall.Signal) error {
	if err := syscall.Kill(-pid, signal); err != nil {
		if procErr := syscall.Kill(pid, signal); procErr != nil {
			return fmt.Errorf("process group: %v; process: %w", err, procErr)
		}
	}
	return nil
}

func (c *Client) restoreManagedServices() {
	data, err := os.ReadFile(c.procStore)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("read managed services store", "path", c.procStore, "error", err)
		}
		return
	}
	var procs []*managedProcess
	if err := json.Unmarshal(data, &procs); err != nil {
		slog.Warn("parse managed services store", "path", c.procStore, "error", err)
		return
	}

	c.procMu.Lock()
	defer c.procMu.Unlock()
	c.restoreManagedServicesFromListLocked(procs)
	c.saveManagedServicesLocked()
	slog.Info("restored managed services", "count", len(c.procs), "store", c.procStore)
}

func (c *Client) restoreManagedServicesFromStoreLocked() {
	data, err := os.ReadFile(c.procStore)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("read managed services store", "path", c.procStore, "error", err)
		}
		return
	}
	var procs []*managedProcess
	if err := json.Unmarshal(data, &procs); err != nil {
		slog.Warn("parse managed services store", "path", c.procStore, "error", err)
		return
	}
	c.restoreManagedServicesFromListLocked(procs)
	c.saveManagedServicesLocked()
}

func (c *Client) restoreManagedServicesFromListLocked(procs []*managedProcess) {
	for _, proc := range procs {
		if proc == nil || proc.PID <= 0 {
			continue
		}
		if !managedProcessAlive(proc) {
			continue
		}
		if proc.OwnerName == "" {
			proc.OwnerName = c.displayName(c.currentConfig(), proc.OwnerUserID)
		}
		c.procs[proc.PID] = proc
	}
}

func (c *Client) saveManagedServicesLocked() {
	if err := os.MkdirAll(filepath.Dir(c.procStore), 0o755); err != nil {
		slog.Warn("create managed services store dir", "path", c.procStore, "error", err)
		return
	}

	procs := make([]*managedProcess, 0, len(c.procs))
	for _, proc := range c.procs {
		if managedProcessAlive(proc) {
			procs = append(procs, proc)
		}
	}
	data, err := json.MarshalIndent(procs, "", "  ")
	if err != nil {
		slog.Warn("marshal managed services store", "error", err)
		return
	}
	if err := os.WriteFile(c.procStore, data, 0o644); err != nil {
		slog.Warn("write managed services store", "path", c.procStore, "error", err)
	}
}

func (c *Client) pruneManagedServicesLocked() {
	changed := false
	for pid, proc := range c.procs {
		if !managedProcessAlive(proc) {
			delete(c.procs, pid)
			changed = true
		}
	}
	if changed {
		c.saveManagedServicesLocked()
	}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func managedProcessAlive(proc *managedProcess) bool {
	if proc == nil || !processAlive(proc.PID) {
		return false
	}
	return processCommandMatches(proc.PID, proc.Command)
}

func processCommandMatches(pid int, command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	output, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.TrimSpace(string(output)), command)
}

func (c *Client) reloadConfig() string {
	next, err := config.Load(c.cfgPath)
	if err != nil {
		return fmt.Sprintf("Reload config failed: %s", err)
	}

	prev := c.currentConfig()
	c.cfgMu.Lock()
	c.cfg = next
	c.cfgMu.Unlock()

	if oldRunner := c.setRunner(codex.NewRunner(next.Codex.Path, next.WorkingDir, next.Session.MaxIdleMinutes)); oldRunner != nil {
		oldRunner.Close()
	}

	c.procMu.Lock()
	c.procStore = filepath.Join(next.WorkingDir, ".maple_bridge", "managed_services.json")
	c.restoreManagedServicesFromStoreLocked()
	c.procMu.Unlock()

	var notes []string
	if prev.Feishu.AppID != next.Feishu.AppID || prev.Feishu.AppSecret != next.Feishu.AppSecret {
		notes = append(notes, "Feishu app_id/app_secret changed; restart bridge to reconnect websocket with the new app.")
	}
	if prev.LogLevel != next.LogLevel {
		notes = append(notes, "log_level changed; restart bridge to rebuild logger and Feishu client log levels.")
	}
	msg := "Config reloaded."
	if len(notes) > 0 {
		msg += "\n" + strings.Join(notes, "\n")
	}
	return msg
}

func (c *Client) currentConfig() *config.Config {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	return c.cfg
}

func (c *Client) currentRunner() *codex.Runner {
	c.runnerMu.RLock()
	defer c.runnerMu.RUnlock()
	return c.runner
}

func (c *Client) setRunner(runner *codex.Runner) *codex.Runner {
	c.runnerMu.Lock()
	old := c.runner
	c.runner = runner
	c.runnerMu.Unlock()
	return old
}

func (c *Client) setRootContext(ctx context.Context) {
	c.rootCtxMu.Lock()
	c.rootCtx = ctx
	c.rootCtxMu.Unlock()
}

func (c *Client) backgroundContext() context.Context {
	c.rootCtxMu.RLock()
	defer c.rootCtxMu.RUnlock()
	if c.rootCtx != nil {
		return c.rootCtx
	}
	return context.Background()
}

func larkLogLevel(level string) larkcore.LogLevel {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return larkcore.LogLevelDebug
	case "warn":
		return larkcore.LogLevelWarn
	case "error":
		return larkcore.LogLevelError
	default:
		return larkcore.LogLevelInfo
	}
}

func isDebugLogLevel(level string) bool {
	return strings.EqualFold(strings.TrimSpace(level), "debug")
}

func resolveExistingDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("%s: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", abs)
	}
	realPath, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return realPath, nil
}

func (c *Client) ensureWorkDirAllowed(session sessionRef, isSuperAdmin bool) error {
	if isSuperAdmin {
		return nil
	}

	realWorkspace, err := resolveExistingDir(c.currentConfig().WorkingDir)
	if err != nil {
		return fmt.Errorf("workspace is invalid: %w", err)
	}

	runner := c.currentRunner()
	realCurrent, err := resolveExistingDir(runner.GetWorkDir(session.Key, session.Persistent))
	if err != nil || !isPathWithin(realWorkspace, realCurrent) {
		runner.SetWorkDir(session.Key, session.Persistent, realWorkspace)
		return nil
	}
	if realCurrent != runner.GetWorkDir(session.Key, session.Persistent) {
		runner.SetWorkDir(session.Key, session.Persistent, realCurrent)
	}
	return nil
}

func isPathWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func helpText() string {
	return strings.Join([]string{
		"Built-in commands:",
		"/help - show this help",
		"/reload - reload config from disk (super-admin only)",
		"/reset - clear your Codex context and state",
		"/ll - list current work directory",
		"/cd <dir> - switch your work directory (super-admin: any dir, others: workspace only)",
		"/workspace - list configured workspace root directory",
		"/status - show Codex context status",
		"/model - show Codex CLI path",
		"/run <command> - run a one-shot shell command, 30s timeout (admin only)",
		"/start <command> - start a managed background service (admin only)",
		"/services - list managed background services (admin: own, super-admin: all)",
		"/pid - list managed service PIDs (admin: own, super-admin: all)",
		"/logs <pid> - show recent logs for a managed service (admin: own, super-admin: all)",
		"/stop <pid> - stop a managed service (admin: own, super-admin: all)",
		"",
		"Non-command messages are sent to Codex.",
	}, "\n")
}

func (c *Client) serviceLogs(userID string, isSuperAdmin bool, pidText string) string {
	var pid int
	if _, err := fmt.Sscanf(pidText, "%d", &pid); err != nil || pid <= 0 {
		return "Usage: /logs <pid>"
	}

	c.procMu.Lock()
	proc, ok := c.procs[pid]
	c.procMu.Unlock()
	if !ok {
		return fmt.Sprintf("Managed service not found: %d", pid)
	}
	if !managedProcessAlive(proc) {
		c.procMu.Lock()
		delete(c.procs, pid)
		c.saveManagedServicesLocked()
		c.procMu.Unlock()
		return fmt.Sprintf("Managed service is no longer running: %d", pid)
	}
	if !canAccessProcess(userID, isSuperAdmin, proc) {
		return fmt.Sprintf("Permission denied: PID %d was started by %s (%s)", pid, proc.OwnerName, proc.OwnerUserID)
	}

	data, err := os.ReadFile(proc.LogPath)
	if err != nil {
		return fmt.Sprintf("Read log failed: %s", err)
	}
	if len(data) == 0 {
		return "(log is empty)"
	}
	if len(data) > 3500 {
		data = data[len(data)-3500:]
	}
	return string(data)
}

// isBotMentioned checks if the bot is mentioned in the message.
func (c *Client) isBotMentioned(msg *larkim.EventMessage) bool {
	if msg.Mentions == nil {
		return false
	}
	for _, m := range msg.Mentions {
		if m.Id != nil && m.Id.OpenId != nil && m.MentionedType != nil && *m.MentionedType == "bot" {
			return true
		}
	}
	return false
}

func (c *Client) isAllowed(cfg *config.Config, userID string) bool {
	if cfg.AllowAllUsers {
		return true
	}
	for _, u := range cfg.AllowedUsers {
		if u == userID {
			return true
		}
	}
	return false
}

func (c *Client) displayName(cfg *config.Config, userID string) string {
	if cfg.UserNames != nil {
		if name := strings.TrimSpace(cfg.UserNames[userID]); name != "" {
			return name
		}
	}
	return userID
}

func (c *Client) isAdmin(cfg *config.Config, userID string) bool {
	if c.isSuperAdmin(cfg, userID) {
		return true
	}
	for _, u := range cfg.AdminUsers {
		if u == userID {
			return true
		}
	}
	return false
}

func (c *Client) isSuperAdmin(cfg *config.Config, userID string) bool {
	for _, u := range cfg.SuperAdmins {
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
		resp, err := c.apiClient.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
			ReceiveIdType("chat_id").
			Body(larkim.NewCreateMessageReqBodyBuilder().
				MsgType("text").
				ReceiveId(chatID).
				Content(string(content)).
				Build()).
			Build())
		if err != nil {
			slog.Error("send message", "error", err)
			continue
		}
		if resp == nil || !resp.Success() {
			slog.Error("send message failed", "response", resp)
		}
	}
}

func (c *Client) sendReplyText(ctx context.Context, messageID, chatID, text string) bool {
	if messageID == "" {
		c.sendText(ctx, chatID, text)
		return false
	}

	chunks := splitMessage(text, 3500)
	for i, chunk := range chunks {
		content, _ := json.Marshal(map[string]string{"text": chunk})
		resp, err := c.apiClient.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().
			MessageId(messageID).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType("text").
				Content(string(content)).
				ReplyInThread(false).
				Uuid(replyUUID(messageID, i)).
				Build()).
			Build())
		if err != nil {
			slog.Error("reply message", "message_id", messageID, "chunk", i, "error", err)
			c.sendText(ctx, chatID, strings.Join(chunks[i:], ""))
			return false
		}
		if resp == nil || !resp.Success() {
			slog.Error("reply message failed", "message_id", messageID, "chunk", i, "response", resp)
			c.sendText(ctx, chatID, strings.Join(chunks[i:], ""))
			return false
		}
	}
	return true
}

func replyUUID(messageID string, chunk int) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s:%d", messageID, chunk)))
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

func (c *Client) sendStatusCard(ctx context.Context, chatID, title, body, request string) string {
	content := statusCardContent(title, body, request)
	resp, err := c.apiClient.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			MsgType("interactive").
			ReceiveId(chatID).
			Content(content).
			Build()).
		Build())
	if err != nil {
		slog.Error("send status card", "error", err)
		return ""
	}
	if resp == nil || !resp.Success() || resp.Data == nil || resp.Data.MessageId == nil {
		slog.Error("send status card failed", "response", resp)
		return ""
	}
	return *resp.Data.MessageId
}

func (c *Client) patchStatusCard(ctx context.Context, messageID, title, body, request string) bool {
	content := statusCardContent(title, body, request)
	resp, err := c.apiClient.Im.Message.Patch(ctx, larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(&larkim.PatchMessageReqBody{Content: &content}).
		Build())
	if err != nil {
		slog.Error("patch status card", "message_id", messageID, "error", err)
		return false
	}
	if resp == nil || !resp.Success() {
		slog.Error("patch status card failed", "message_id", messageID, "response", resp)
		return false
	}
	return true
}

func statusCardContent(title, body, request string) string {
	card := map[string]any{
		"config": map[string]any{
			"wide_screen_mode": true,
			"update_multi":     true,
		},
		"header": map[string]any{
			"title": map[string]string{
				"tag":     "plain_text",
				"content": title,
			},
		},
		"elements": []map[string]any{
			{
				"tag": "div",
				"text": map[string]string{
					"tag":     "lark_md",
					"content": statusCardText(body),
				},
			},
		},
	}
	if request != "" {
		card["elements"] = append(card["elements"].([]map[string]any), map[string]any{
			"tag":      "note",
			"elements": []map[string]string{{"tag": "plain_text", "content": "Request: " + truncate(request, 120)}},
		})
	}
	data, _ := json.Marshal(card)
	return string(data)
}

func statusCardText(text string) string {
	if text == "" {
		text = "(empty)"
	}
	if len(text) > 12000 {
		text = truncateWithNotice(text, 12000)
	}
	return text
}

func extractText(content string) string {
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(content), &data); err != nil {
		return strings.TrimSpace(content)
	}
	if text, ok := data["text"].(string); ok {
		// Strip @mention placeholders like @_user_1
		text = mentionRegex.ReplaceAllString(text, "")
		return strings.TrimSpace(text)
	}
	if blocks, ok := data["content"].([]interface{}); ok {
		var parts []string
		for _, block := range blocks {
			items, ok := block.([]interface{})
			if !ok {
				continue
			}
			for _, item := range items {
				obj, ok := item.(map[string]interface{})
				if !ok {
					continue
				}
				if tag, _ := obj["tag"].(string); tag != "text" {
					continue
				}
				if text, ok := obj["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		if len(parts) > 0 {
			return strings.TrimSpace(strings.Join(parts, ""))
		}
	}
	return content
}

var (
	mentionRegex     = regexp.MustCompile(`@_user_\d+\s*`)
	secretTokenRegex = regexp.MustCompile(`\b(?:sk|SK)-[A-Za-z0-9_-]{12,}\b`)
)

type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
	total int
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.total += len(p)
	if b.limit <= 0 || b.buf.Len() >= b.limit {
		return len(p), nil
	}
	remaining := b.limit - b.buf.Len()
	if len(p) > remaining {
		p = p[:remaining]
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *limitedBuffer) Len() int {
	return b.total
}

func (b *limitedBuffer) String() string {
	text := b.buf.String()
	if b.total > b.limit {
		text = validPrefix(text, b.limit) + fmt.Sprintf("\n... (truncated, total %d bytes)", b.total)
	}
	return text
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return validPrefix(s, n) + "..."
}

func truncateWithNotice(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return validPrefix(s, n) + fmt.Sprintf("\n... (truncated, total %d bytes)", len(s))
}

func validPrefix(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.ValidString(s[:n]) {
		n--
	}
	if n <= 0 {
		return ""
	}
	return s[:n]
}

func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}
	var chunks []string
	for len(text) > maxLen {
		chunk := validPrefix(text, maxLen)
		if chunk == "" {
			chunk = text[:maxLen]
		}
		chunks = append(chunks, chunk)
		text = text[len(chunk):]
	}
	if text != "" {
		chunks = append(chunks, text)
	}
	return chunks
}
