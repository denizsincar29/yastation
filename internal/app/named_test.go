package app

import (
	"context"
	"testing"
)

// TestExecuteNamedSay checks the HTTP/MCP-shaped entry point: no slash
// line is built or parsed, values arrive already named.
func TestExecuteNamedSay(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	out, ok, err := a.ExecuteNamed(context.Background(), "say", "Кухня", map[string]string{"text": "привет"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("say should have a bound handler")
	}
	if out != "Алиса сказала: привет" {
		t.Fatalf("out=%q", out)
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "say:Кухня:привет" {
		t.Fatalf("calls=%v", calls)
	}
}

// TestExecuteNamedVolume checks a numeric-shaped param arriving as a
// plain named string value.
func TestExecuteNamedVolume(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	_, ok, err := a.ExecuteNamed(context.Background(), "volume", "", map[string]string{"level": "3"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("volume should have a bound handler")
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "volume::3" {
		t.Fatalf("calls=%v", calls)
	}
}

// TestExecuteNamedNotifyDefaultVolume checks the bound (HTTP/MCP) path
// for /notify: volume is just a named field, defaulting to 4 when
// absent — same default as the legacy REPL path.
func TestExecuteNamedNotifyDefaultVolume(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	_, ok, err := a.ExecuteNamed(context.Background(), "notify", "", map[string]string{"text": "готово"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("notify should have a bound handler")
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "notify::готово:4" {
		t.Fatalf("calls=%v", calls)
	}
}

// TestExecuteNamedNotifyExplicitVolume checks the bound path with an
// explicit volume field.
func TestExecuteNamedNotifyExplicitVolume(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	_, _, err := a.ExecuteNamed(context.Background(), "notify", "", map[string]string{"text": "готово", "volume": "7"})
	if err != nil {
		t.Fatal(err)
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "notify::готово:7" {
		t.Fatalf("calls=%v", calls)
	}
}

// TestNotifyLegacyVolumeKeywordAnywhere makes sure refactoring /notify
// onto HandleNamed didn't change its documented REPL syntax (README.md):
// volume=X can still appear anywhere in the slash-line args, not just
// as the first positional token.
func TestNotifyLegacyVolumeKeywordAnywhere(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	_, err := a.Execute(context.Background(), "/notify задача выполнена volume=7")
	if err != nil {
		t.Fatal(err)
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "notify::задача выполнена:7" {
		t.Fatalf("calls=%v", calls)
	}
}

// TestNotifyLegacyDefaultVolume checks the REPL path's default when
// volume= is omitted entirely.
func TestNotifyLegacyDefaultVolume(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	_, err := a.Execute(context.Background(), "/notify привет")
	if err != nil {
		t.Fatal(err)
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "notify::привет:4" {
		t.Fatalf("calls=%v", calls)
	}
}

// TestExecuteNamedUnknownCommand checks ok=false (not an error) for a
// name with no bound handler at all.
func TestExecuteNamedUnknownCommand(t *testing.T) {
	a, _ := newTestApp()
	defer a.Close()
	_, ok, err := a.ExecuteNamed(context.Background(), "totally_not_a_command", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected ok=false for an unregistered command")
	}
}

// TestExecuteNamedScenario checks a command with no station concept at
// all (takesStation=false) still works through ExecuteNamed.
func TestExecuteNamedScenario(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	out, _, err := a.ExecuteNamed(context.Background(), "scenario", "", map[string]string{"name": "Вечер"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "[сценарий запущен] Вечер" {
		t.Fatalf("out=%q", out)
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "scenario:Вечер" {
		t.Fatalf("calls=%v", calls)
	}
}

// TestExecuteNamedCustomCommand checks a config/commands.json-driven
// command (registered via registerCustomCommand -> HandleBoundCat) is
// also directly callable by name with no slash-line involved.
func TestExecuteNamedCustomCommand(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	if err := a.RegisterCustomCommands(&CustomCommandConfig{Commands: []CustomCommandDef{{
		Name:     "english",
		Params:   []string{"text"},
		Template: `Say this in English: "$text"`,
		Kind:     "command",
	}}}); err != nil {
		t.Fatal(err)
	}
	_, ok, err := a.ExecuteNamed(context.Background(), "english", "", map[string]string{"text": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("english should have a bound handler")
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != `cmd::Say this in English: "hello"` {
		t.Fatalf("calls=%v", calls)
	}
}
