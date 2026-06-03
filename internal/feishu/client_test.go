package feishu

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/nnn5130/maple_bridge/internal/codex"
	"github.com/nnn5130/maple_bridge/internal/config"
)

func TestChangeWorkDirRestrictsNonSuperAdminToWorkspace(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	outside := t.TempDir()
	client := testClient(t, workspace)
	session := testSession()

	msg := client.changeWorkDir(session, outside, false)
	if !strings.Contains(msg, "Permission denied") {
		t.Fatalf("expected permission denial, got %q", msg)
	}

	if got := client.currentRunner().GetWorkDir(session.Key, session.Persistent); got != workspace {
		t.Fatalf("expected workdir to remain %q, got %q", workspace, got)
	}
}

func TestChangeWorkDirAllowsSuperAdminOutsideWorkspace(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	outside := t.TempDir()
	client := testClient(t, workspace)
	session := testSession()

	msg := client.changeWorkDir(session, outside, true)
	if !strings.Contains(msg, "Working directory changed") {
		t.Fatalf("expected successful cd, got %q", msg)
	}

	realOutside, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatalf("resolve outside dir: %v", err)
	}
	if got := client.currentRunner().GetWorkDir(session.Key, session.Persistent); got != realOutside {
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
	session := testSession()

	msg := client.changeWorkDir(session, link, false)
	if !strings.Contains(msg, "Permission denied") {
		t.Fatalf("expected symlink escape denial, got %q", msg)
	}
}

func TestEnsureWorkDirAllowedResetsNonSuperAdminOutsideWorkspace(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	outside := t.TempDir()
	client := testClient(t, workspace)
	session := testSession()
	client.currentRunner().SetWorkDir(session.Key, session.Persistent, outside)

	if err := client.ensureWorkDirAllowed(session, false); err != nil {
		t.Fatalf("ensure workdir: %v", err)
	}

	realWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	if got := client.currentRunner().GetWorkDir(session.Key, session.Persistent); got != realWorkspace {
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

func TestFinalDeliveryForChatTypeAndOutputLength(t *testing.T) {
	t.Parallel()

	if got := finalDeliveryFor("group", "short output"); got != finalDeliveryReply {
		t.Fatalf("expected group delivery %q, got %q", finalDeliveryReply, got)
	}
	if got := finalDeliveryFor("p2p", "short output"); got != finalDeliveryCard {
		t.Fatalf("expected private short delivery %q, got %q", finalDeliveryCard, got)
	}
	if got := finalDeliveryFor("p2p", strings.Repeat("x", cardPreviewLimit+1)); got != finalDeliveryText {
		t.Fatalf("expected private long delivery %q, got %q", finalDeliveryText, got)
	}
}

func TestFinalCardBodyForGroupUsesSummary(t *testing.T) {
	t.Parallel()

	body := finalCardBody(finalDeliveryReply, "Codex finished", "full answer should not be embedded", 2*time.Second)
	if !strings.Contains(body, "结果已在下方回复") {
		t.Fatalf("expected reply summary, got %q", body)
	}
	if strings.Contains(body, "full answer should not be embedded") {
		t.Fatalf("group card should not embed the full answer: %q", body)
	}
}

func TestFinalCardBodyForFailureUsesFailedStatus(t *testing.T) {
	t.Parallel()

	body := finalCardBody(finalDeliveryText, "Codex failed", strings.Repeat("x", cardPreviewLimit+1), time.Second)
	if !strings.Contains(body, "处理失败") {
		t.Fatalf("expected failed status, got %q", body)
	}
}

func TestReplyUUIDIsStableUUIDShape(t *testing.T) {
	t.Parallel()

	got := replyUUID("om_test", 2)
	if got != replyUUID("om_test", 2) {
		t.Fatal("expected reply uuid to be stable")
	}
	parts := strings.Split(got, "-")
	lengths := []int{8, 4, 4, 4, 12}
	if len(parts) != len(lengths) {
		t.Fatalf("expected uuid shape, got %q", got)
	}
	for i, part := range parts {
		if len(part) != lengths[i] {
			t.Fatalf("unexpected uuid part %d length in %q", i, got)
		}
	}
}

func TestMessageSessionUsesChatForNonTopicMessages(t *testing.T) {
	t.Parallel()

	chatID := "oc_test"
	session := messageSession(&larkim.EventMessage{ChatId: &chatID})
	if session.Key != "chat:oc_test" || session.Persistent {
		t.Fatalf("unexpected chat session: %#v", session)
	}
}

func TestMessageSessionUsesPersistentTopicForRootMessages(t *testing.T) {
	t.Parallel()

	chatID := "oc_test"
	rootID := "om_root"
	session := messageSession(&larkim.EventMessage{ChatId: &chatID, RootId: &rootID})
	if session.Key != "topic:oc_test:om_root" || !session.Persistent {
		t.Fatalf("unexpected topic session: %#v", session)
	}
}

func TestUserFacingErrorStripsCommandOutput(t *testing.T) {
	t.Parallel()

	err := errors.New("codex cli failed: exit status 101\nstderr: long internal log\nstdout: noisy output")
	got := userFacingError(err)
	if got != "Error: codex cli failed: exit status 101" {
		t.Fatalf("unexpected user-facing error: %q", got)
	}
}

func TestRedactSensitiveHidesAPIKeys(t *testing.T) {
	t.Parallel()

	input := "use api key sk-exampleSecretToken123456 and keep normal text"
	got := redactSensitive(input)
	if strings.Contains(got, "sk-exampleSecretToken123456") {
		t.Fatalf("expected secret to be redacted: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") || !strings.Contains(got, "keep normal text") {
		t.Fatalf("unexpected redacted text: %q", got)
	}
}

func TestExtractTextFromPostContent(t *testing.T) {
	t.Parallel()

	content := `{"title":"","content":[[{"tag":"at","user_id":"@_user_1","user_name":"螃蟹助手"},{"tag":"text","text":"现在的session管理方案"}]]}`
	got := extractText(content)
	if got != "现在的session管理方案" {
		t.Fatalf("unexpected extracted text: %q", got)
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

func testSession() sessionRef {
	return sessionRef{Key: "chat:oc_test", Label: "chat oc_test"}
}
