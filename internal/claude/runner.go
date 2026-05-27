package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"
)

type Result struct {
	Text      string  `json:"result"`
	SessionID string  `json:"session_id"`
	IsError   bool    `json:"is_error"`
	CostUSD   float64 `json:"total_cost_usd"`
}

type Runner struct {
	claudePath string
	workDir    string

	mu       sync.Map // map[string]*sessionInfo
	maxIdle  time.Duration
}

type sessionInfo struct {
	sessionID  string
	lastUsed   time.Time
	mu         sync.Mutex
}

func NewRunner(claudePath, workDir string, maxIdleMin int) *Runner {
	r := &Runner{
		claudePath: claudePath,
		workDir:    workDir,
		maxIdle:    time.Duration(maxIdleMin) * time.Minute,
	}
	go r.cleanup()
	return r
}

// Run executes claude -p with the user's message, resuming their session if one exists.
func (r *Runner) Run(ctx context.Context, userID, message string) (*Result, error) {
	info := r.getOrCreate(userID)
	info.mu.Lock()
	defer info.mu.Unlock()
	info.lastUsed = time.Now()

	args := []string{
		"-p",
		"--output-format", "json",
		"--permission-mode", "bypassPermissions",
	}

	if info.sessionID != "" {
		args = append(args, "-r", info.sessionID)
	}

	args = append(args, message)

	slog.Info("running claude", "session_id", info.sessionID, "message_len", len(message))

	cmd := exec.CommandContext(ctx, r.claudePath, args...)
	cmd.Dir = r.workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("claude cli failed: %w\nstderr: %s", err, stderr.String())
	}

	var result Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("parse claude output: %w\nraw: %s", err, stdout.String())
	}

	// Save session ID for multi-turn
	if result.SessionID != "" && info.sessionID == "" {
		info.sessionID = result.SessionID
	}

	return &result, nil
}

func (r *Runner) Reset(userID string) {
	r.mu.Delete(userID)
}

func (r *Runner) getOrCreate(userID string) *sessionInfo {
	val, ok := r.mu.Load(userID)
	if ok {
		return val.(*sessionInfo)
	}
	info := &sessionInfo{}
	r.mu.Store(userID, info)
	return info
}

func (r *Runner) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		r.mu.Range(func(key, value any) bool {
			info := value.(*sessionInfo)
			if now.Sub(info.lastUsed) > r.maxIdle {
				slog.Info("session expired", "user_id", key)
				r.mu.Delete(key)
			}
			return true
		})
	}
}
