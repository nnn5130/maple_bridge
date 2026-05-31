package codex

import (
	"bytes"
	"context"
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
	maxHistoryContentBytes = 32 * 1024
)

type Result struct {
	Text    string
	IsError bool
}

type Runner struct {
	codexPath string
	workDir   string

	mu       sync.Map // map[string]*sessionInfo
	maxIdle  time.Duration
	stop     chan struct{}
	stopOnce sync.Once
}

type sessionInfo struct {
	lastUsed time.Time
	turns    int
	workDir  string
	history  []historyMessage
	mu       sync.Mutex
}

type historyMessage struct {
	Role      string
	Content   string
	CreatedAt time.Time
}

func NewRunner(codexPath, workDir string, maxIdleMin int) *Runner {
	r := &Runner{
		codexPath: codexPath,
		workDir:   workDir,
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
func (r *Runner) Run(ctx context.Context, userID, message string, isAdmin bool) (*Result, error) {
	info := r.getOrCreate(userID)
	info.mu.Lock()
	defer info.mu.Unlock()
	info.touch()
	info.pruneHistory(r.maxIdle)

	wd := info.workDir
	if wd == "" {
		wd = r.workDir
	}
	prompt := info.promptWithHistory(message, r.maxIdle)

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
	info.history = append(info.history,
		historyMessage{Role: "user", Content: limitHistoryContent(message), CreatedAt: time.Now()},
		historyMessage{Role: "assistant", Content: limitHistoryContent(string(output)), CreatedAt: time.Now()},
	)
	info.trimHistory(maxHistoryMessages)
	info.touch()

	return &Result{Text: string(output)}, nil
}

func (r *Runner) Reset(userID string) {
	r.mu.Delete(userID)
}

// SessionInfo returns the current per-user runner state.
func (r *Runner) SessionInfo(userID string) (sessionID, workDir string, turns int) {
	info := r.getOrCreate(userID)
	info.mu.Lock()
	defer info.mu.Unlock()
	info.touch()
	wd := info.workDir
	if wd == "" {
		wd = r.workDir
	}
	info.pruneHistory(r.maxIdle)
	return fmt.Sprintf("codex exec with %d-minute bridge context (%d messages)", int(r.maxIdle.Minutes()), len(info.history)), wd, info.turns
}

// SetWorkDir updates the working directory for a user's session.
func (r *Runner) SetWorkDir(userID, dir string) {
	info := r.getOrCreate(userID)
	info.mu.Lock()
	defer info.mu.Unlock()
	info.touch()
	info.workDir = filepath.Clean(dir)
}

// GetWorkDir returns the effective working directory for a user.
func (r *Runner) GetWorkDir(userID string) string {
	info := r.getOrCreate(userID)
	info.mu.Lock()
	defer info.mu.Unlock()
	info.touch()
	if info.workDir != "" {
		return info.workDir
	}
	return r.workDir
}

func (r *Runner) getOrCreate(userID string) *sessionInfo {
	val, ok := r.mu.Load(userID)
	if ok {
		return val.(*sessionInfo)
	}
	info := &sessionInfo{lastUsed: time.Now()}
	actual, _ := r.mu.LoadOrStore(userID, info)
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

func (s *sessionInfo) promptWithHistory(message string, maxAge time.Duration) string {
	if len(s.history) == 0 {
		return message
	}

	var b strings.Builder
	fmt.Fprintf(&b, "You are continuing a Feishu-controlled Codex conversation. The following context contains messages from the last %d minutes. Use it as conversation history, but prioritize the latest user request.\n\n", int(maxAge.Minutes()))
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
			if expired {
				slog.Info("session expired", "user_id", key)
				r.mu.Delete(key)
			}
			return true
		})
	}
}
