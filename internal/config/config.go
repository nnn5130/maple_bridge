package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Feishu        FeishuConfig      `yaml:"feishu"`
	WorkingDir    string            `yaml:"working_dir"`
	AllowAllUsers bool              `yaml:"allow_all_users"`
	AdminUsers    []string          `yaml:"admin_users"`
	SuperAdmins   []string          `yaml:"super_admin_users"`
	AllowedUsers  []string          `yaml:"allowed_users"`
	UserNames     map[string]string `yaml:"user_names"`
	Session       SessionConfig     `yaml:"session"`
	Codex         CodexConfig       `yaml:"codex"`
	LogLevel      string            `yaml:"log_level"`
}

type FeishuConfig struct {
	AppID     string `yaml:"app_id"`
	AppSecret string `yaml:"app_secret"`
}

type CodexConfig struct {
	Path string `yaml:"path"`
}

type SessionConfig struct {
	MaxIdleMinutes int `yaml:"max_idle_minutes"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := &Config{
		LogLevel: "info",
		Session: SessionConfig{
			MaxIdleMinutes: 30,
		},
		Codex: CodexConfig{
			Path: "codex",
		},
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) validate() error {
	if c.Feishu.AppID == "" {
		return fmt.Errorf("feishu.app_id is required")
	}
	if c.Feishu.AppSecret == "" {
		return fmt.Errorf("feishu.app_secret is required")
	}
	if !c.AllowAllUsers && len(c.AllowedUsers) == 0 {
		return fmt.Errorf("allowed_users is required unless allow_all_users is true")
	}
	if c.WorkingDir == "" {
		return fmt.Errorf("working_dir is required")
	}
	if c.Session.MaxIdleMinutes <= 0 {
		return fmt.Errorf("session.max_idle_minutes must be greater than 0")
	}
	info, err := os.Stat(c.WorkingDir)
	if err != nil {
		return fmt.Errorf("working_dir %q: %w", c.WorkingDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("working_dir %q is not a directory", c.WorkingDir)
	}
	return nil
}
