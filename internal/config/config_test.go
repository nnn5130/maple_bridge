package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRequiresAllowedUsersUnlessAllowAllUsers(t *testing.T) {
	t.Parallel()

	configPath := writeConfig(t, `
feishu:
  app_id: "cli_test"
  app_secret: "secret"
working_dir: "`+filepath.ToSlash(t.TempDir())+`"
allow_all_users: false
allowed_users: []
`)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "allowed_users is required") {
		t.Fatalf("expected allowed_users error, got %v", err)
	}
}

func TestLoadAllowsExplicitAllowAllUsers(t *testing.T) {
	t.Parallel()

	configPath := writeConfig(t, `
feishu:
  app_id: "cli_test"
  app_secret: "secret"
working_dir: "`+filepath.ToSlash(t.TempDir())+`"
allow_all_users: true
allowed_users: []
`)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.AllowAllUsers {
		t.Fatal("expected AllowAllUsers to be true")
	}
}

func TestLoadRejectsNonPositiveMaxIdleMinutes(t *testing.T) {
	t.Parallel()

	configPath := writeConfig(t, `
feishu:
  app_id: "cli_test"
  app_secret: "secret"
working_dir: "`+filepath.ToSlash(t.TempDir())+`"
allow_all_users: true
session:
  max_idle_minutes: 0
`)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "session.max_idle_minutes") {
		t.Fatalf("expected max_idle_minutes error, got %v", err)
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
