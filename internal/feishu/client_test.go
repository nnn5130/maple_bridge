package feishu

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitee.com/maple_wsy/maple_bridge/internal/codex"
	"gitee.com/maple_wsy/maple_bridge/internal/config"
)

func TestChangeWorkDirRestrictsNonSuperAdminToWorkspace(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	outside := t.TempDir()
	client := testClient(workspace)

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
	client := testClient(workspace)

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
	client := testClient(workspace)

	msg := client.changeWorkDir("ou_test", link, false)
	if !strings.Contains(msg, "Permission denied") {
		t.Fatalf("expected symlink escape denial, got %q", msg)
	}
}

func TestEnsureWorkDirAllowedResetsNonSuperAdminOutsideWorkspace(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	outside := t.TempDir()
	client := testClient(workspace)
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

func testClient(workspace string) *Client {
	return &Client{
		cfg: &config.Config{
			WorkingDir: workspace,
		},
		runner: codex.NewRunner("codex", workspace, 30),
	}
}
