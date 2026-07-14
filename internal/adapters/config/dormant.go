package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// Save writes cfg to ~/.rune/config.json (atomic-ish via O_TRUNC + 0o600).
//
// Truncate + write in one syscall, perm 0600. Not a true atomic-rename
// pattern — if the process is killed mid-write the file is left truncated.
// For dormant-state updates this is acceptable: the next boot will read
// whatever's left and either succeed (active state) or re-attempt the
// dormant write.
func Save(cfg *Config) error {
	configPath, err := DefaultConfigPath()
	if err != nil {
		return err
	}
	return SaveToPath(cfg, configPath)
}

// SaveToPath writes cfg to a specific path (used by tests).
func SaveToPath(cfg *Config, path string) error {
	if err := EnsureDirectories(); err != nil {
		return fmt.Errorf("config: ensure directories: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}

	if err := os.WriteFile(path, data, FilePerm); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}

// MarkDormant transitions config.json to dormant state with the given reason
// + RFC3339 UTC timestamp, so the daemon's view of "why am I dormant"
// survives process restarts (next boot reads the same dormant_reason).
//
// Idempotent: if config.json is already dormant with the same reason, this
// is a no-op (no disk write).
//
// If config.json doesn't exist yet (fresh install), this creates it with
// just the dormant fields populated. The Console section stays zero so the
// next /rune:configure run can fill it in normally.
//
// Use cases (called by boot loop on terminal failures):
//   - "not_configured"     — config.json missing, fresh install
//   - "console_unconfigured" — config exists but Console.Endpoint/Token empty
//   - "user_deactivated"   — already-dormant config picked up by boot
//     (idempotent path, just refreshes timestamp)
func MarkDormant(reason string) error {
	cfg, err := Load()
	if err != nil {
		// Either the file is missing (fresh install) or it's
		// corrupt/unreadable. Both cases: fall back to a fresh Config —
		// overwriting bad state with a clean dormant config is the right
		// recovery here, not bubbling up.
		if !errors.Is(err, fs.ErrNotExist) {
			// Surface unexpected errors (permission, IO) to the caller; only
			// missing-file gets the silent fallback path.
			//
			// Note: parse errors are wrapped by LoadFromPath but the
			// underlying chain still resolves through errors.Is for
			// fs.ErrNotExist. For any other failure, prefer creating a fresh
			// dormant record over crashing the boot loop.
		}
		cfg = &Config{}
	}

	if cfg.State == "dormant" && cfg.DormantReason == reason {
		return nil // already in this state — skip write
	}

	cfg.State = "dormant"
	cfg.DormantReason = reason
	cfg.DormantSince = time.Now().UTC().Format(time.RFC3339)

	return Save(cfg)
}

// ClearDormant transitions config.json back to the active state and removes the
// dormant_reason / dormant_since markers. It is the inverse of MarkDormant.
//
// Called by Activate when the user explicitly resumes a daemon they put to
// sleep with /rune:deactivate. /rune:activate expresses intent to come back
// online, so the persisted user_deactivated marker must be cleared — otherwise
// the boot loop reads config.State != "active" (lifecycle/boot.go) and
// immediately re-enters dormant, ignoring the activation.
//
// Idempotent: if config.json is already active with no dormant markers, this is
// a no-op (no disk write), mirroring MarkDormant's "skip if same" guard.
func ClearDormant() error {
	cfg, err := Load()
	if err != nil {
		return err
	}

	if cfg.State == "active" && cfg.DormantReason == "" && cfg.DormantSince == "" {
		return nil // already active — skip write
	}

	cfg.State = "active"
	cfg.DormantReason = ""
	cfg.DormantSince = ""

	return Save(cfg)
}
