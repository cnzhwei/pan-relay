package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	RefreshToken string `json:"refresh_token"`
	AccessToken  string `json:"access_token"`
	FamilyID     string `json:"family_id,omitempty"`
	RootFolderID string `json:"root_folder_id,omitempty"`
	ZoneURL      string `json:"zone_url,omitempty"`
	BaseURL      string `json:"base_url,omitempty"`

	filePath string `json:"-"`
}

func Load(customPath string) (*Config, error) {
	var targetPath string
	candidates := []string{}

	if customPath != "" {
		candidates = append(candidates, customPath)
	}

	// Current dir
	candidates = append(candidates, "./wopan_config.json")

	// Home dir
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".config", "wopan-cli", "config.json"),
			filepath.Join(home, ".wopan_config.json"),
		)
	}

	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			targetPath = p
			break
		}
	}

	cfg := &Config{
		RootFolderID: "0",
		ZoneURL:      "https://tjupload.pan.wo.cn",
		BaseURL:      "https://panservice.mail.wo.cn",
	}

	if targetPath != "" {
		data, err := os.ReadFile(targetPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read config file %s: %w", targetPath, err)
		}
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config JSON in %s: %w", targetPath, err)
		}
		cfg.filePath = targetPath
	}

	// Environment variable overrides
	if env := os.Getenv("WOPAN_REFRESH_TOKEN"); env != "" {
		cfg.RefreshToken = env
	}
	if env := os.Getenv("WOPAN_ACCESS_TOKEN"); env != "" {
		cfg.AccessToken = env
	}
	if env := os.Getenv("WOPAN_FAMILY_ID"); env != "" {
		cfg.FamilyID = env
	}
	if env := os.Getenv("WOPAN_ROOT_FOLDER_ID"); env != "" {
		cfg.RootFolderID = env
	}
	if env := os.Getenv("WOPAN_ZONE_URL"); env != "" {
		cfg.ZoneURL = env
	}

	if cfg.RefreshToken == "" && cfg.AccessToken == "" {
		return nil, fmt.Errorf("no WoPan credentials found; provide wopan_config.json or set WOPAN_REFRESH_TOKEN environment variable")
	}

	return cfg, nil
}

func (c *Config) Save() error {
	if c.filePath == "" {
		// If no file was read, try saving to ~/.config/wopan-cli/config.json
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		dir := filepath.Join(home, ".config", "wopan-cli")
		_ = os.MkdirAll(dir, 0700)
		c.filePath = filepath.Join(dir, "config.json")
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	tmpFile := c.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmpFile, c.filePath)
}

func (c *Config) FilePath() string {
	return c.filePath
}
