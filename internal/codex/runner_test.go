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
