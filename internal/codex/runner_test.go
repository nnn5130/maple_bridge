package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestLimitHistoryContentKeepsUTF8Valid(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("你", maxHistoryContentBytes)
	got := limitHistoryContent(text)
	if !utf8.ValidString(got) {
		t.Fatalf("history content is not valid utf-8")
	}
	if len(got) >= len(text) {
		t.Fatalf("expected history content to be truncated")
	}
}

func TestTrimHistoryKeepsNewestMessages(t *testing.T) {
	t.Parallel()

	info := &sessionInfo{
		history: []historyMessage{
			{Role: "user", Content: "1"},
			{Role: "assistant", Content: "2"},
			{Role: "user", Content: "3"},
		},
	}
	info.trimHistory(2)
	if len(info.history) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(info.history))
	}
	if info.history[0].Content != "2" || info.history[1].Content != "3" {
		t.Fatalf("unexpected history after trim: %#v", info.history)
	}
}

func TestRecordTurnKeepsOnlyUserMessagesForSharedChat(t *testing.T) {
	t.Parallel()

	info := &sessionInfo{}
	for i := 0; i < maxHistoryMessages+5; i++ {
		info.recordTurn("user message", strings.Repeat("assistant output", 100), false)
	}
	if len(info.history) != maxHistoryMessages {
		t.Fatalf("expected %d history messages, got %d", maxHistoryMessages, len(info.history))
	}
	for _, msg := range info.history {
		if msg.Role != "user" {
			t.Fatalf("expected only user messages in shared chat history, got %#v", info.history)
		}
	}
}

func TestRecordTurnKeepsAssistantMessagesForPersistentTopics(t *testing.T) {
	t.Parallel()

	info := &sessionInfo{}
	info.recordTurn("user message", "assistant output", true)
	if len(info.history) != 2 {
		t.Fatalf("expected user and assistant messages, got %#v", info.history)
	}
	if info.history[0].Role != "user" || info.history[1].Role != "assistant" {
		t.Fatalf("unexpected persistent topic history: %#v", info.history)
	}
}

func TestPromptIncludesBridgeImageGenerationConstraintWithoutHistory(t *testing.T) {
	t.Parallel()

	info := &sessionInfo{}
	got := info.promptWithHistory("画一张图", 30, false)
	if !strings.Contains(got, "Do not use the built-in image_gen tool") {
		t.Fatalf("expected image_gen constraint, got %q", got)
	}
	if !strings.Contains(got, "image generation is not supported") {
		t.Fatalf("expected unsupported fallback instruction, got %q", got)
	}
	if !strings.Contains(got, "画一张图") {
		t.Fatalf("expected latest user message, got %q", got)
	}
}

func TestPromptIncludesBridgeImageGenerationConstraintWithHistory(t *testing.T) {
	t.Parallel()

	info := &sessionInfo{
		history: []historyMessage{{Role: "assistant", Content: "old answer"}},
	}
	got := info.promptWithHistory("latest", 30, false)
	if !strings.Contains(got, "Do not use the built-in image_gen tool") {
		t.Fatalf("expected image_gen constraint, got %q", got)
	}
	if !strings.Contains(got, "<conversation_context>") || !strings.Contains(got, "<latest_user_request>") {
		t.Fatalf("expected history prompt structure, got %q", got)
	}
}

func TestPersistentSessionRestoresWorkDir(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	other := t.TempDir()
	key := "topic:oc_test:om_root"

	runner := NewRunner("codex", workspace, 30)
	runner.SetWorkDir(key, true, other)
	runner.Close()

	restored := NewRunner("codex", workspace, 30)
	t.Cleanup(restored.Close)
	if got := restored.GetWorkDir(key, true); got != other {
		t.Fatalf("expected persisted workdir %q, got %q", other, got)
	}
}

func TestPersistentSessionResetRemovesStoredState(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	other := t.TempDir()
	key := "topic:oc_test:om_root"

	runner := NewRunner("codex", workspace, 30)
	t.Cleanup(runner.Close)
	runner.SetWorkDir(key, true, other)
	if _, err := os.Stat(runner.sessionPath(key)); err != nil {
		t.Fatalf("expected session file: %v", err)
	}
	runner.Reset(key, true)
	if _, err := os.Stat(runner.sessionPath(key)); !os.IsNotExist(err) {
		t.Fatalf("expected session file to be removed, got %v", err)
	}
}

func TestRunTimesOut(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	script := filepath.Join(workspace, "codex-stub.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 2\n"), 0o755); err != nil {
		t.Fatalf("write codex stub: %v", err)
	}

	runner := NewRunner(script, workspace, 30)
	runner.runTimeout = 10 * time.Millisecond
	t.Cleanup(runner.Close)

	_, err := runner.Run(context.Background(), "chat:timeout", false, "hello", false)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestCleanupRemovesResetSessionOnlyWhenNotRunning(t *testing.T) {
	t.Parallel()

	runner := NewRunner("codex", t.TempDir(), 30)
	t.Cleanup(runner.Close)
	key := "chat:reset"
	info := runner.getOrCreate(key, false)

	info.runMu.Lock()
	runner.Reset(key, false)
	runner.expireIdleSessions(time.Now())
	if _, ok := runner.mu.Load(key); !ok {
		t.Fatal("running reset session should remain until run finishes")
	}

	info.runMu.Unlock()
	runner.expireIdleSessions(time.Now())
	if _, ok := runner.mu.Load(key); ok {
		t.Fatal("reset session should be removed after run finishes")
	}
}

func TestCodexFailureErrorTruncatesOutput(t *testing.T) {
	t.Parallel()

	err := codexFailureError(assertionError("boom"), strings.Repeat("s", commandErrorLimit+10), strings.Repeat("o", commandErrorLimit+10))
	text := err.Error()
	if !strings.Contains(text, "truncated") {
		t.Fatalf("expected truncated notice, got %q", text)
	}
	if len(text) > commandErrorLimit*3 {
		t.Fatalf("error text is unexpectedly large: %d", len(text))
	}
}

func TestLimitedBufferWriteReportsOriginalLength(t *testing.T) {
	t.Parallel()

	buf := newLimitedBuffer(3)
	n, err := buf.Write([]byte("abcdef"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != 6 {
		t.Fatalf("expected original write length 6, got %d", n)
	}
	if got := buf.String(); !strings.Contains(got, "abc") || !strings.Contains(got, "truncated") {
		t.Fatalf("unexpected buffer text: %q", got)
	}
}

type assertionError string

func (e assertionError) Error() string {
	return string(e)
}
