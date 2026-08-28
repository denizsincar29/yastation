package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadHotkeyConfigValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hotkeys.json")
	if err := os.WriteFile(path, []byte(`{"bindings":[
		{"key":"ctrl+o","command":"music"},
		{"key":"ctrl+v","command":"next"},
		{"key":"ctrl+x","command":"sound взрыв"}
	]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadHotkeyConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Bindings) != 3 {
		t.Fatalf("bindings=%v", cfg.Bindings)
	}
}

func TestLoadHotkeyConfigUnknownKeyRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hotkeys.json")
	if err := os.WriteFile(path, []byte(`{"bindings":[{"key":"ctrl+p","command":"music"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadHotkeyConfig(path); err == nil {
		t.Fatal("expected an error for ctrl+p (already claimed by readline for history navigation)")
	}
}

func TestLoadHotkeyConfigMissingCommandRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hotkeys.json")
	if err := os.WriteFile(path, []byte(`{"bindings":[{"key":"ctrl+o","command":""}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadHotkeyConfig(path); err == nil {
		t.Fatal("expected an error for a missing command")
	}
}

func TestLoadHotkeyConfigDuplicateKeyRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hotkeys.json")
	if err := os.WriteFile(path, []byte(`{"bindings":[
		{"key":"ctrl+o","command":"music"},
		{"key":"ctrl+o","command":"scenarios"}
	]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadHotkeyConfig(path); err == nil {
		t.Fatal("expected an error for ctrl+o bound twice")
	}
}

func TestEnsureHotkeyFileSeedsEmptyDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hotkeys.json")
	if err := EnsureHotkeyFile(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadHotkeyConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Bindings) != 0 {
		t.Fatalf("expected an empty default, got %v", cfg.Bindings)
	}

	// EnsureHotkeyFile never touches an already-existing file again.
	if err := os.WriteFile(path, []byte(`{"bindings":[{"key":"ctrl+o","command":"music"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureHotkeyFile(path); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadHotkeyConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Bindings) != 1 {
		t.Fatalf("EnsureHotkeyFile should not have overwritten an existing file: %v", cfg.Bindings)
	}
}

func TestHotkeyRune(t *testing.T) {
	for _, name := range ValidHotkeyNames {
		if _, ok := HotkeyRune(name); !ok {
			t.Fatalf("HotkeyRune should recognize every name in ValidHotkeyNames, missed %q", name)
		}
	}
	if _, ok := HotkeyRune("ctrl+p"); ok {
		t.Fatal("HotkeyRune should not recognize ctrl+p (claimed by readline)")
	}
}
