package codex

import (
	"strings"
	"testing"
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

func TestPromptIncludesBridgeImageGenerationConstraintWithoutHistory(t *testing.T) {
	t.Parallel()

	info := &sessionInfo{}
	got := info.promptWithHistory("画一张图", 30)
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
	got := info.promptWithHistory("latest", 30)
	if !strings.Contains(got, "Do not use the built-in image_gen tool") {
		t.Fatalf("expected image_gen constraint, got %q", got)
	}
	if !strings.Contains(got, "<conversation_context>") || !strings.Contains(got, "<latest_user_request>") {
		t.Fatalf("expected history prompt structure, got %q", got)
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

type assertionError string

func (e assertionError) Error() string {
	return string(e)
}
