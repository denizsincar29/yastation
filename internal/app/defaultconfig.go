package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	yastation "github.com/denizsincar29/yastation"
)

// ConfigFilePath resolves where the live, user-editable set of built-in
// template commands lives: $YASTATION_CONFIG_FILE if set, otherwise
// <user config dir>/yastation/config.json — same convention as
// quasar.TokenFilePath (YASTATION_TOKEN_FILE / tokens.json).
//
// This is a different file from the one --config/YASTATION_COMMANDS_FILE
// points at: that one is for a user's own extra commands (see
// examples/commands.json); config.json is yastation's own built-ins
// (play/pause/timer/... — see config.json.default), made editable instead
// of hardcoded.
func ConfigFilePath() string {
	if p := os.Getenv("YASTATION_CONFIG_FILE"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "yastation", "config.json")
}

// EnsureConfigFile makes sure path exists, seeding it from the embedded
// config.json.default on first run. It never touches the file again once
// it exists — from that point on it's entirely the user's to edit
// (rename/delete it and it'll be reseeded from the default next run).
func EnsureConfigFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, yastation.DefaultCommandsJSON, 0o644)
}

// DefaultCommandsConfig parses the embedded config.json.default directly,
// without touching disk. Used by tests (so they don't write into the real
// user config dir) and by anything else that just wants the built-in
// commands without the copy-on-first-run/edit-in-place behaviour that
// ConfigFilePath + EnsureConfigFile give the REPL/server.
func DefaultCommandsConfig() (*CustomCommandConfig, error) {
	var cfg CustomCommandConfig
	if err := json.Unmarshal(yastation.DefaultCommandsJSON, &cfg); err != nil {
		return nil, fmt.Errorf("встроенный config.json.default повреждён: %w", err)
	}
	return &cfg, nil
}
