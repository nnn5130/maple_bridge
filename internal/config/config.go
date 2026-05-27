package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Feishu       FeishuConfig    `yaml:"feishu"`
	WorkingDir   string          `yaml:"working_dir"`
	AllowedUsers []string        `yaml:"allowed_users"`
	Session      SessionConfig   `yaml:"session"`
	Claude       ClaudeConfig    `yaml:"claude"`
	LogLevel     string          `yaml:"log_level"`
}

type FeishuConfig struct {
	AppID     string `yaml:"app_id"`
	AppSecret string `yaml:"app_secret"`
}

type ClaudeConfig struct {
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
			MaxIdleMinutes: 60,
		},
		Claude: ClaudeConfig{
			Path: "claude",
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
	if c.WorkingDir == "" {
		return fmt.Errorf("working_dir is required")
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
