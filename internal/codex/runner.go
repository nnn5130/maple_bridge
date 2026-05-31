package codex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	commandOutputLimit     = 64 * 1024
	commandErrorLimit      = 4000
	maxHistoryMessages     = 20
	maxHistoryContentBytes = 8 * 1024
)

const bridgeInstructions = `Bridge runtime constraints:
- Do not use the built-in image_gen tool from codex exec.
- For image-generation or image-editing requests, use a configured image-generation skill or API only when one is available.
- If no configured image-generation skill or API is available, tell the user that image generation is not supported in this bridge environment.
`

type Result struct {
	Text    string
	IsError bool
}

type Runner struct {
	codexPath string
	workDir   string
	storeDir  string

	mu       sync.Map // map[string]*sessionInfo, keyed by chat/topic session key.
	maxIdle  time.Duration
	stop     chan struct{}
	stopOnce sync.Once
}

type sessionInfo struct {
	lastUsed time.Time
	turns    int
	workDir  string
	history  []historyMessage
	mu       sync.Mutex `json:"-"`
}

type historyMessage struct {
	Role      string
	Content   string
	CreatedAt time.Time
}

type persistedSession struct {
	LastUsed time.Time        `json:"last_used"`
	Turns    int              `json:"turns"`
	WorkDir  string           `json:"work_dir,omitempty"`
	History  []historyMessage `json:"history,omitempty"`
}

func NewRunner(codexPath, workDir string, maxIdleMin int) *Runner {
	r := &Runner{
		codexPath: codexPath,
		workDir:   workDir,
		storeDir:  filepath.Join(workDir, ".maple_bridge", "topic_sessions"),
		maxIdle:   time.Duration(maxIdleMin) * time.Minute,
		stop:      make(chan struct{}),
	}
	go r.cleanup()
	return r
}

// Close stops background cleanup for this runner.
func (r *Runner) Close() {
	r.stopOnce.Do(func() {
		close(r.stop)
	})
}

// Run executes Codex CLI non-interactively for a single Feishu message.
func (r *Runner) Run(ctx context.Context, sessionKey string, persistent bool, message string, isAdmin bool) (*Result, error) {
	info := r.getOrCreate(sessionKey, persistent)
	info.mu.Lock()
	defer info.mu.Unlock()
	info.touch()
	if !persistent {
		info.pruneHistory(r.maxIdle)
	}

	wd := info.workDir
	if wd == "" {
		wd = r.workDir
	}
	prompt := info.promptWithHistory(message, r.maxIdle, persistent)

	outputFile, err := os.CreateTemp("", "maple-bridge-codex-*.txt")
	if err != nil {
		return nil, fmt.Errorf("create codex output file: %w", err)
	}
	outputPath := outputFile.Name()
	_ = outputFile.Close()
	defer os.Remove(outputPath)

	args := []string{
		"exec",
		"--cd", wd,
		"--output-last-message", outputPath,
		"--color", "never",
		"--skip-git-repo-check",
	}
	if isAdmin {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	} else {
		args = append(args, "--sandbox", "workspace-write")
	}
	args = append(args, "-")

	slog.Info("running codex", "work_dir", wd, "message_len", len(message), "prompt_len", len(prompt), "history_messages", len(info.history), "admin", isAdmin)

	cmd := exec.CommandContext(ctx, r.codexPath, args...)
	cmd.Dir = wd
	cmd.Stdin = strings.NewReader(prompt)

	stdout := newLimitedBuffer(commandOutputLimit)
	stderr := newLimitedBuffer(commandOutputLimit)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		return nil, codexFailureError(err, stderr.String(), stdout.String())
	}

	output, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read codex output: %w", err)
	}
	info.turns++
	info.recordTurn(message, string(output), persistent)
	info.touch()
	if persistent {
		if err := r.saveSessionLocked(sessionKey, info); err != nil {
			slog.Warn("save persistent session", "session_key", sessionKey, "error", err)
		}
	}

	return &Result{Text: string(output)}, nil
}

func (r *Runner) Reset(sessionKey string, persistent bool) {
	r.mu.Delete(sessionKey)
	if persistent {
		if err := os.Remove(r.sessionPath(sessionKey)); err != nil && !os.IsNotExist(err) {
			slog.Warn("remove persistent session", "session_key", sessionKey, "error", err)
		}
	}
}

// SessionInfo returns the current chat/topic runner state.
func (r *Runner) SessionInfo(sessionKey string, persistent bool) (sessionID, workDir string, turns int) {
	info := r.getOrCreate(sessionKey, persistent)
	info.mu.Lock()
	defer info.mu.Unlock()
	info.touch()
	wd := info.workDir
	if wd == "" {
		wd = r.workDir
	}
	if !persistent {
		info.pruneHistory(r.maxIdle)
	}
	if persistent {
		return fmt.Sprintf("persistent topic context (%d messages)", len(info.history)), wd, info.turns
	}
	return fmt.Sprintf("codex exec with %d-minute chat context (%d messages)", int(r.maxIdle.Minutes()), len(info.history)), wd, info.turns
}

// SetWorkDir updates the working directory for a chat/topic session.
func (r *Runner) SetWorkDir(sessionKey string, persistent bool, dir string) {
	info := r.getOrCreate(sessionKey, persistent)
	info.mu.Lock()
	defer info.mu.Unlock()
	info.touch()
	info.workDir = filepath.Clean(dir)
	if persistent {
		if err := r.saveSessionLocked(sessionKey, info); err != nil {
			slog.Warn("save persistent session workdir", "session_key", sessionKey, "error", err)
		}
	}
}

// GetWorkDir returns the effective working directory for a chat/topic session.
func (r *Runner) GetWorkDir(sessionKey string, persistent bool) string {
	info := r.getOrCreate(sessionKey, persistent)
	info.mu.Lock()
	defer info.mu.Unlock()
	info.touch()
	if info.workDir != "" {
		return info.workDir
	}
	return r.workDir
}

func (r *Runner) getOrCreate(sessionKey string, persistent bool) *sessionInfo {
	val, ok := r.mu.Load(sessionKey)
	if ok {
		return val.(*sessionInfo)
	}
	info := &sessionInfo{lastUsed: time.Now()}
	if persistent {
		if loaded, err := r.loadSession(sessionKey); err == nil {
			info = loaded
		} else if !os.IsNotExist(err) {
			slog.Warn("load persistent session", "session_key", sessionKey, "error", err)
		}
		if info.lastUsed.IsZero() {
			info.lastUsed = time.Now()
		}
	}
	actual, _ := r.mu.LoadOrStore(sessionKey, info)
	return actual.(*sessionInfo)
}

func (s *sessionInfo) touch() {
	s.lastUsed = time.Now()
}

func (s *sessionInfo) pruneHistory(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	kept := s.history[:0]
	for _, msg := range s.history {
		if msg.CreatedAt.After(cutoff) {
			kept = append(kept, msg)
		}
	}
	s.history = kept
}

func (s *sessionInfo) trimHistory(maxMessages int) {
	if maxMessages > 0 && len(s.history) > maxMessages {
		s.history = s.history[len(s.history)-maxMessages:]
	}
}

func (s *sessionInfo) recordTurn(message, output string, persistent bool) {
	now := time.Now()
	s.history = append(s.history, historyMessage{Role: "user", Content: limitHistoryContent(message), CreatedAt: now})
	if persistent {
		s.history = append(s.history, historyMessage{Role: "assistant", Content: limitHistoryContent(output), CreatedAt: now})
	}
	s.trimHistory(maxHistoryMessages)
}

func (s *sessionInfo) promptWithHistory(message string, maxAge time.Duration, persistent bool) string {
	var b strings.Builder
	b.WriteString(bridgeInstructions)
	b.WriteString("\n")

	if len(s.history) == 0 {
		b.WriteString(message)
		return b.String()
	}

	if persistent {
		b.WriteString("You are continuing a persistent Feishu topic Codex conversation. Use the following context as shared topic history visible to everyone in the topic, but prioritize the latest user request.\n\n")
	} else {
		fmt.Fprintf(&b, "You are continuing a Feishu-controlled Codex chat conversation. The following context contains messages from the last %d minutes and is shared by the chat. Use it as conversation history, but prioritize the latest user request.\n\n", int(maxAge.Minutes()))
	}
	b.WriteString("<conversation_context>\n")
	for _, msg := range s.history {
		fmt.Fprintf(&b, "%s: %s\n\n", msg.Role, msg.Content)
	}
	b.WriteString("</conversation_context>\n\n")
	b.WriteString("<latest_user_request>\n")
	b.WriteString(message)
	b.WriteString("\n</latest_user_request>")
	return b.String()
}

func (r *Runner) loadSession(sessionKey string) (*sessionInfo, error) {
	data, err := os.ReadFile(r.sessionPath(sessionKey))
	if err != nil {
		return nil, err
	}
	var info sessionInfo
	var persisted persistedSession
	if err := json.Unmarshal(data, &persisted); err != nil {
		return nil, err
	}
	info.lastUsed = persisted.LastUsed
	info.turns = persisted.Turns
	info.workDir = persisted.WorkDir
	info.history = persisted.History
	info.trimHistory(maxHistoryMessages)
	return &info, nil
}

func (r *Runner) saveSessionLocked(sessionKey string, info *sessionInfo) error {
	if err := os.MkdirAll(r.storeDir, 0o755); err != nil {
		return err
	}
	persisted := persistedSession{
		LastUsed: info.lastUsed,
		Turns:    info.turns,
		WorkDir:  info.workDir,
		History:  info.history,
	}
	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.sessionPath(sessionKey), data, 0o600)
}

func (r *Runner) sessionPath(sessionKey string) string {
	sum := sha256.Sum256([]byte(sessionKey))
	return filepath.Join(r.storeDir, hex.EncodeToString(sum[:])+".json")
}

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

func (b *limitedBuffer) String() string {
	text := b.buf.String()
	if b.total > b.limit {
		text = validPrefix(text, b.limit) + fmt.Sprintf("\n... (truncated, total %d bytes)", b.total)
	}
	return text
}

func limitHistoryContent(s string) string {
	if len(s) <= maxHistoryContentBytes {
		return s
	}
	return validPrefix(s, maxHistoryContentBytes) + fmt.Sprintf("\n... (truncated, total %d bytes)", len(s))
}

func codexFailureError(err error, stderr, stdout string) error {
	return fmt.Errorf("codex cli failed: %w\nstderr: %s\nstdout: %s", err, truncateWithNotice(stderr, commandErrorLimit), truncateWithNotice(stdout, commandErrorLimit))
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

func (r *Runner) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-r.stop:
			return
		}
		now := time.Now()
		r.mu.Range(func(key, value any) bool {
			info := value.(*sessionInfo)
			info.mu.Lock()
			expired := now.Sub(info.lastUsed) > r.maxIdle
			info.mu.Unlock()
			sessionKey, _ := key.(string)
			if expired && !strings.HasPrefix(sessionKey, "topic:") {
				slog.Info("session expired", "session_key", key)
				r.mu.Delete(key)
			}
			return true
		})
	}
}
