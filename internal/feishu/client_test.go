package feishu

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"gitee.com/maple_wsy/maple_bridge/internal/codex"
	"gitee.com/maple_wsy/maple_bridge/internal/config"
)

func TestChangeWorkDirRestrictsNonSuperAdminToWorkspace(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	outside := t.TempDir()
	client := testClient(t, workspace)

	msg := client.changeWorkDir("ou_test", outside, false)
	if !strings.Contains(msg, "Permission denied") {
		t.Fatalf("expected permission denial, got %q", msg)
	}

	if got := client.currentRunner().GetWorkDir("ou_test"); got != workspace {
		t.Fatalf("expected workdir to remain %q, got %q", workspace, got)
	}
}

func TestChangeWorkDirAllowsSuperAdminOutsideWorkspace(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	outside := t.TempDir()
	client := testClient(t, workspace)

	msg := client.changeWorkDir("ou_test", outside, true)
	if !strings.Contains(msg, "Working directory changed") {
		t.Fatalf("expected successful cd, got %q", msg)
	}

	realOutside, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatalf("resolve outside dir: %v", err)
	}
	if got := client.currentRunner().GetWorkDir("ou_test"); got != realOutside {
		t.Fatalf("expected workdir %q, got %q", realOutside, got)
	}
}

func TestChangeWorkDirRejectsSymlinkEscapeForNonSuperAdmin(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(workspace, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink not available: %v", err)
	}
	client := testClient(t, workspace)

	msg := client.changeWorkDir("ou_test", link, false)
	if !strings.Contains(msg, "Permission denied") {
		t.Fatalf("expected symlink escape denial, got %q", msg)
	}
}

func TestEnsureWorkDirAllowedResetsNonSuperAdminOutsideWorkspace(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	outside := t.TempDir()
	client := testClient(t, workspace)
	client.currentRunner().SetWorkDir("ou_test", outside)

	if err := client.ensureWorkDirAllowed("ou_test", false); err != nil {
		t.Fatalf("ensure workdir: %v", err)
	}

	realWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	if got := client.currentRunner().GetWorkDir("ou_test"); got != realWorkspace {
		t.Fatalf("expected workdir %q, got %q", realWorkspace, got)
	}
}

func TestIsAllowedRequiresExplicitUserUnlessAllowAllUsers(t *testing.T) {
	t.Parallel()

	client := &Client{}
	if client.isAllowed(&config.Config{}, "ou_test") {
		t.Fatal("expected user to be denied without allow_all_users or explicit allowlist")
	}
	if !client.isAllowed(&config.Config{AllowAllUsers: true}, "ou_test") {
		t.Fatal("expected user to be allowed when allow_all_users is true")
	}
	if !client.isAllowed(&config.Config{AllowedUsers: []string{"ou_test"}}, "ou_test") {
		t.Fatal("expected explicitly allowed user")
	}
}

func TestMarkMessageSeenDedupesAndExpiresOldEntries(t *testing.T) {
	t.Parallel()

	client := &Client{
		seenMsgs: map[string]time.Time{
			"old-message": time.Now().Add(-messageDedupeTTL - time.Second),
		},
	}
	if !client.markMessageSeen("message-1") {
		t.Fatal("expected first message to be accepted")
	}
	if client.markMessageSeen("message-1") {
		t.Fatal("expected duplicate message to be rejected")
	}
	if _, ok := client.seenMsgs["old-message"]; ok {
		t.Fatal("expected old message id to be pruned")
	}
}

func TestStatusCardContentUsesDivTextSchema(t *testing.T) {
	t.Parallel()

	var card map[string]any
	if err := json.Unmarshal([]byte(statusCardContent("done", "body", "request")), &card); err != nil {
		t.Fatalf("parse card: %v", err)
	}
	elements, ok := card["elements"].([]any)
	if !ok || len(elements) == 0 {
		t.Fatalf("expected card elements, got %#v", card["elements"])
	}
	first, ok := elements[0].(map[string]any)
	if !ok {
		t.Fatalf("expected first element map, got %#v", elements[0])
	}
	text, ok := first["text"].(map[string]any)
	if !ok {
		t.Fatalf("expected div text object, got %#v", first)
	}
	if text["tag"] != "lark_md" || text["content"] != "body" {
		t.Fatalf("unexpected div text: %#v", text)
	}
}

func TestSplitMessageKeepsUTF8Valid(t *testing.T) {
	t.Parallel()

	chunks := splitMessage(strings.Repeat("你", 2000), 3500)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for _, chunk := range chunks {
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk is not valid utf-8: %q", chunk)
		}
	}
}

func testClient(t *testing.T, workspace string) *Client {
	t.Helper()
	runner := codex.NewRunner("codex", workspace, 30)
	t.Cleanup(runner.Close)
	return &Client{
		cfg: &config.Config{
			WorkingDir: workspace,
		},
		runner: runner,
	}
}
