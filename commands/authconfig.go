package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultConfigFileName = "auth.config"

// AuthConfig holds per-server authentication tokens.
// The file format is a JSON object mapping normalized server URLs to entries.
type AuthConfig struct {
	Servers map[string]AuthEntry `json:"servers"`
}

// AuthEntry stores the auth token for a single server.
type AuthEntry struct {
	Token string `json:"token"`
}

// defaultConfigPath returns ~/.pocketci/auth.config.
func defaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}

	return filepath.Join(home, ".pocketci", defaultConfigFileName), nil
}

// LoadAuthConfig reads the config file from disk.
// Returns an empty config (not an error) when the file does not exist.
func LoadAuthConfig(path string) (*AuthConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &AuthConfig{Servers: make(map[string]AuthEntry)}, nil
		}

		return nil, fmt.Errorf("could not read auth config %s: %w", path, err)
	}

	var cfg AuthConfig
	unmarshalErr := json.Unmarshal(data, &cfg)
	if unmarshalErr != nil {
		return nil, fmt.Errorf("could not parse auth config %s: %w", path, unmarshalErr)
	}

	if cfg.Servers == nil {
		cfg.Servers = make(map[string]AuthEntry)
	}

	return &cfg, nil
}

// SaveAuthConfig writes the config file to disk, creating parent directories.
func SaveAuthConfig(path string, cfg *AuthConfig) error {
	dir := filepath.Dir(path)

	mkdirErr := os.MkdirAll(dir, 0o700)
	if mkdirErr != nil {
		return fmt.Errorf("could not create config directory: %w", mkdirErr)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("could not marshal auth config: %w", err)
	}

	writeErr := os.WriteFile(path, data, 0o600)
	if writeErr != nil {
		return fmt.Errorf("could not write auth config: %w", writeErr)
	}

	return nil
}

// normalizeServerURL trims trailing slashes so lookups are consistent.
func normalizeServerURL(serverURL string) string {
	return strings.TrimSuffix(serverURL, "/")
}

// ResolveAuthToken returns the effective auth token for a given server URL.
// Priority: explicit flag/env > config file lookup.
// configPath may be "" to use the default path.
func ResolveAuthToken(explicitToken, configPath, serverURL string) string {
	if explicitToken != "" {
		return explicitToken
	}

	if configPath == "" {
		var err error

		configPath, err = defaultConfigPath()
		if err != nil {
			return ""
		}
	}

	cfg, err := LoadAuthConfig(configPath)
	if err != nil {
		return ""
	}

	key := normalizeServerURL(serverURL)

	if entry, ok := cfg.Servers[key]; ok {
		return entry.Token
	}

	return ""
}
