// Package config loads ~/.rune/config.json (3-section schema, Go v0.4).
//
// Dropped sections:
//
//	runespace / embedding / llm / scribe / retriever — moved to Console bundle
//	(memory only) or external embedder process.
package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/keyring"
)

// Config — top-level. Read-only by rune-mcp (write path: /rune:configure CLI).
type Config struct {
	Console ConsoleConfig `json:"console"`

	// Deprecated: for backward compatibility which use previous "vault"
	LegacyVault *ConsoleConfig `json:"vault,omitempty"`

	State         string         `json:"state"` // "active" | "dormant"
	DormantReason string         `json:"dormant_reason,omitempty"`
	DormantSince  string         `json:"dormant_since,omitempty"` // RFC3339 UTC
	Metadata      map[string]any `json:"metadata,omitempty"`      // configVersion/lastUpdated/installedFrom
}

// Token storage modes for ConsoleConfig.TokenStorage.
const (
	// TokenStorageConfig keeps the token in this file (Token field). Also the
	// implied mode for legacy configs written before keyring support (Token set,
	// TokenStorage empty).
	TokenStorageConfig = "config"
	// TokenStorageKeyring keeps the token in the OS keyring; the Token field is
	// blank and the value is read from the keyring keyed by Endpoint.
	TokenStorageKeyring = "keyring"
)

// ConsoleConfig — connection + auth.
type ConsoleConfig struct {
	Endpoint string `json:"endpoint"` // tcp://host:port | http(s)://... | host[:port]
	// Token holds the access token when TokenStorage != "keyring". It is blank
	// when the token lives in the OS keyring.
	Token      string `json:"token,omitempty"`
	CACert     string `json:"ca_cert,omitempty"`
	TLSDisable bool   `json:"tls_disable,omitempty"`
	// TokenStorage selects where the token is read from: "keyring" (OS secret
	// store, keyed by Endpoint) or "config"/"" (the Token field above).
	TokenStorage string `json:"token_storage,omitempty"`
}

// FilePerms:
//
//	~/.rune/               0700
//	~/.rune/config.json    0600
const (
	DirPerm  = 0700
	FilePerm = 0600
)

func DormantParsedSince(c *Config) time.Time {
	if c.DormantSince == "" {
		return time.Time{}
	}

	// DormantSince to time.Time
	t, _ := time.Parse(time.RFC3339, c.DormantSince)
	return t
}

func (c *Config) IsActive() bool {
	return c.State == "active"
}

// ResolveToken returns the effective console token. When TokenStorage is
// "keyring" it reads the OS keyring (keyed by Endpoint); otherwise it returns
// the in-file Token. A keyring miss or an unusable keyring is an error — the
// caller configured keyring storage, so silently falling back to an empty token
// would connect unauthenticated.
func (c *Config) ResolveToken() (string, error) {
	if c.Console.TokenStorage != TokenStorageKeyring {
		return c.Console.Token, nil
	}
	tok, ok, err := keyring.Get(c.Console.Endpoint)
	if err != nil {
		return "", fmt.Errorf("config: read token from keyring: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("config: token_storage=keyring but no keyring entry for %q (re-run /rune:configure)", c.Console.Endpoint)
	}
	return tok, nil
}

func RuneDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: UserHomeDir: %w", err)
	}
	return filepath.Join(home, ".rune"), nil // ~/.rune
}

func DefaultConfigPath() (string, error) {
	dir, err := RuneDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil // ~/.rune/config.json
}

// ConsoleCAPath is where the bootstrap persists the console's pinned CA.
func ConsoleCAPath() (string, error) {
	dir, err := RuneDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "console-ca.pem"), nil // ~/.rune/console-ca.pem
}

// SaveConsoleCA writes the pinned CA PEM to ~/.rune/console-ca.pem (0600) and
// returns its path. The path is what /rune:configure stores as console.ca_cert
// so subsequent connections verify against the pinned anchor.
func SaveConsoleCA(pem []byte) (string, error) {
	if err := EnsureDirectories(); err != nil {
		return "", fmt.Errorf("config: ensure directories: %w", err)
	}
	path, err := ConsoleCAPath()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, pem, FilePerm); err != nil {
		return "", fmt.Errorf("config: write CA %s: %w", path, err)
	}
	return path, nil
}

func Load() (*Config, error) {
	configPath, err := DefaultConfigPath()
	if err != nil {
		return nil, err
	}
	return LoadFromPath(configPath)
}

func LoadFromPath(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	migrateLegacyVault(&cfg, path)

	if stateOverride := os.Getenv("RUNE_STATE"); stateOverride != "" {
		cfg.State = stateOverride
	}

	return &cfg, nil
}

func migrateLegacyVault(cfg *Config, path string) {
	// In-place update of old "vault" to new "console"
	if cfg.LegacyVault != nil && cfg.Console == (ConsoleConfig{}) {
		cfg.Console = *cfg.LegacyVault
		slog.Warn(`config: the "vault" section is deprecated and migrated to "console"; re-run /rune:configure to rewrite file`,
			"path", path)
	}

	cfg.LegacyVault = nil
}

func EnsureDirectories() error {
	dir, err := RuneDir()
	if err != nil {
		return err
	}

	// Create directory if not exists
	if err := os.MkdirAll(dir, DirPerm); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", dir, err)
	}

	// Force permissions
	if err := os.Chmod(dir, DirPerm); err != nil {
		return fmt.Errorf("config: chmod %s: %w", dir, err)
	}

	// Ensure subdirectories
	for _, sub := range []string{"keys", "logs"} {
		subDir := filepath.Join(dir, sub)
		if err := os.MkdirAll(subDir, DirPerm); err != nil {
			return fmt.Errorf("config: mkdir %s: %w", subDir, err)
		}
		if err := os.Chmod(subDir, DirPerm); err != nil {
			return fmt.Errorf("config: chmod %s: %w", subDir, err)
		}
	}

	return nil
}
