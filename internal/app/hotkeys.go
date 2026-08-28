package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	yastation "github.com/denizsincar29/yastation"
)

// HotkeyDef binds one REPL-only keyboard shortcut to a command line. See
// ValidHotkeyNames for why only three keys are supported right now, and
// cmd/yastation's repl() for where these actually get wired up (via
// readline's Config.FuncFilterInputRune — see that function's own doc
// comment for the full explanation of what is and isn't possible here
// without patching the vendored readline library).
type HotkeyDef struct {
	// Key is one of ValidHotkeyNames, e.g. "ctrl+o".
	Key string `json:"key"`
	// Command is a full command line, same grammar as the REPL/-c flag —
	// the leading "/" is optional and added automatically if missing
	// (see cmd/yastation's normalizeCFlagCommands), so both "music" and
	// "/music" work, and params are just more words: "sound взрыв".
	Command string `json:"command"`
}

// HotkeyConfig is hotkeys.json's shape (see hotkeys.json.default).
type HotkeyConfig struct {
	Bindings []HotkeyDef `json:"bindings"`
}

// hotkeyRunes maps each valid Key name to the raw control-code rune
// readline's Config.FuncFilterInputRune actually observes for it (see
// cmd/yastation's repl()) — plain ASCII control codes (Ctrl+<letter> =
// <letter's ASCII code> - 64) that reach this program directly, no ANSI
// escape parsing involved at all — unlike F-keys/Alt+letter, which
// aren't supported: see ValidHotkeyNames's doc comment for why.
var hotkeyRunes = map[string]rune{
	"ctrl+o": 15,
	"ctrl+v": 22,
	"ctrl+x": 24,
}

// ValidHotkeyNames are the only key names HotkeyDef.Key currently
// accepts. Deliberately a short, hardcoded list: these are the only
// Ctrl+letter combinations *not* already claimed by readline's own
// line-editing — Ctrl+A/B/C/D/E/F/G/H/I/J/K/L/N/P/R/S/T/U/W/Y/Z all do
// something already (history navigation, word/line movement, kill/yank,
// incremental search, ...). Ctrl+Q is skipped too even though it's
// technically free, since many terminals reserve it for XON/XOFF flow
// control and would swallow it before it ever reaches this program.
// F-keys, Alt+letter (besides the couple readline already understands
// for word-jumping) and media keys aren't reachable at all right now —
// readline's ANSI escape parser silently discards anything it doesn't
// already recognize, and some of those (media keys especially) may
// never even reach the terminal in the first place, captured earlier by
// the OS/desktop environment instead. Getting any of that working would
// mean patching the vendored readline library itself, which is a
// deliberately separate, bigger step from this one.
var ValidHotkeyNames = sortedHotkeyNames()

func sortedHotkeyNames() []string {
	names := make([]string, 0, len(hotkeyRunes))
	for n := range hotkeyRunes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// HotkeyRune returns the raw rune readline's Config.FuncFilterInputRune
// observes for name (one of ValidHotkeyNames), and whether name was
// recognized at all.
func HotkeyRune(name string) (rune, bool) {
	r, ok := hotkeyRunes[name]
	return r, ok
}

// HotkeyFilePath resolves where the user's REPL hotkey bindings live:
// $YASTATION_HOTKEYS_FILE if set, otherwise <user config dir>/yastation/
// hotkeys.json — same convention as ConfigFilePath/quasar.TokenFilePath.
func HotkeyFilePath() string {
	if p := os.Getenv("YASTATION_HOTKEYS_FILE"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "yastation", "hotkeys.json")
}

// EnsureHotkeyFile makes sure path exists, seeding it from the embedded
// hotkeys.json.default (empty bindings — nothing is bound out of the
// box, entirely opt-in) on first run. Never touched again once it
// exists, same as EnsureConfigFile.
func EnsureHotkeyFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, yastation.DefaultHotkeysJSON, 0o644)
}

// LoadHotkeyConfig reads and validates path, rejecting an unknown Key
// name, a missing Command, or two bindings on the same key outright
// (naming which one and why) rather than silently keeping only one of
// them.
func LoadHotkeyConfig(path string) (*HotkeyConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("не смог прочитать %s: %w", path, err)
	}
	var cfg HotkeyConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("не смог разобрать %s: %w", path, err)
	}
	seen := make(map[string]bool, len(cfg.Bindings))
	for _, b := range cfg.Bindings {
		if !validHotkeyName(b.Key) {
			return nil, fmt.Errorf("%s: неизвестная клавиша %q — сейчас можно назначать только: %s", path, b.Key, strings.Join(ValidHotkeyNames, ", "))
		}
		if strings.TrimSpace(b.Command) == "" {
			return nil, fmt.Errorf("%s: у клавиши %q не задана команда (\"command\")", path, b.Key)
		}
		if seen[b.Key] {
			return nil, fmt.Errorf("%s: клавиша %q назначена дважды", path, b.Key)
		}
		seen[b.Key] = true
	}
	return &cfg, nil
}

func validHotkeyName(name string) bool {
	for _, n := range ValidHotkeyNames {
		if n == name {
			return true
		}
	}
	return false
}
