package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBindCustomParamsLastIsVariadic(t *testing.T) {
	values, err := bindCustomParams([]string{"lang", "text"}, []string{"english", "hello", "there"})
	if err != nil {
		t.Fatal(err)
	}
	if values["lang"] != "english" || values["text"] != "hello there" {
		t.Fatalf("values=%v", values)
	}
}

func TestBindCustomParamsSingleParamCapturesEverything(t *testing.T) {
	values, err := bindCustomParams([]string{"text"}, []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if values["text"] != "a b c" {
		t.Fatalf("values=%v", values)
	}
}

func TestBindCustomParamsMissingArgsErrors(t *testing.T) {
	_, err := bindCustomParams([]string{"lang", "text"}, []string{"english"})
	if err == nil {
		t.Fatal("expected error for too few args")
	}
}

func TestBindCustomParamsOptionalTrailingParam(t *testing.T) {
	values, err := bindCustomParams([]string{"minutes", "label?"}, []string{"10"})
	if err != nil {
		t.Fatal(err)
	}
	if values["minutes"] != "10" || values["label"] != "" {
		t.Fatalf("values=%v", values)
	}

	values2, err := bindCustomParams([]string{"minutes", "label?"}, []string{"10", "чайник", "кипит"})
	if err != nil {
		t.Fatal(err)
	}
	if values2["minutes"] != "10" || values2["label"] != "чайник кипит" {
		t.Fatalf("values2=%v", values2)
	}
}

func TestBindCustomParamsNoParams(t *testing.T) {
	values, err := bindCustomParams(nil, []string{"игнорируется"})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 0 {
		t.Fatalf("values=%v", values)
	}
}

func TestRenderCustomTemplate(t *testing.T) {
	got := renderCustomTemplate(`Say this exact words in English: "$text"`, map[string]string{"text": "hello there"})
	want := `Say this exact words in English: "hello there"`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRenderCustomTemplateLeavesUnknownPlaceholder(t *testing.T) {
	got := renderCustomTemplate("hi $typo", map[string]string{"text": "x"})
	if got != "hi $typo" {
		t.Fatalf("got %q", got)
	}
}

func TestValidateCustomCommandDef(t *testing.T) {
	cases := []struct {
		name    string
		def     CustomCommandDef
		wantErr bool
	}{
		{"valid", CustomCommandDef{Name: "english", Params: []string{"text"}, Template: "$text"}, false},
		{"no name", CustomCommandDef{Params: []string{"text"}, Template: "$text"}, true},
		{"no params ok (fixed phrase)", CustomCommandDef{Name: "x", Template: "продолжить"}, false},
		{"no template", CustomCommandDef{Name: "x", Params: []string{"text"}}, true},
		{"bad kind", CustomCommandDef{Name: "x", Params: []string{"text"}, Template: "t", Kind: "shout"}, true},
		{"dup params", CustomCommandDef{Name: "x", Params: []string{"a", "a"}, Template: "t"}, true},
		{"trailing optional ok", CustomCommandDef{Name: "x", Params: []string{"a", "b?"}, Template: "t"}, false},
		{"required after optional", CustomCommandDef{Name: "x", Params: []string{"a?", "b"}, Template: "t"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateCustomCommandDef(c.def)
			if (err != nil) != c.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, c.wantErr)
			}
		})
	}
}

func TestRegisterCustomCommandsEndToEnd(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()

	cfg := &CustomCommandConfig{Commands: []CustomCommandDef{
		{
			Name:     "english",
			Aliases:  []string{"en"},
			Params:   []string{"text"},
			Template: `Say this exact words in English: "$text"`,
			Kind:     "command",
		},
		{
			Name:     "translate",
			Params:   []string{"lang", "text"},
			Template: "Переведи на $lang: $text",
			Kind:     "say",
		},
	}}
	if err := a.RegisterCustomCommands(cfg); err != nil {
		t.Fatal(err)
	}

	out := mustExec(t, a, "/english привет мир")
	if out != `[english] Say this exact words in English: "привет мир"` {
		t.Fatalf("out=%q", out)
	}
	mustExec(t, a, "/en заглушка") // alias works too

	out2 := mustExec(t, a, "/translate french привет мир")
	if out2 != "[translate] Переведи на french: привет мир" {
		t.Fatalf("out2=%q", out2)
	}

	calls := f.Calls()
	want := []string{
		`cmd::Say this exact words in English: "привет мир"`,
		`cmd::Say this exact words in English: "заглушка"`,
		"say::Переведи на french: привет мир",
	}
	if len(calls) != len(want) {
		t.Fatalf("calls=%v", calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("call %d: got %q want %q", i, calls[i], want[i])
		}
	}
}

func TestRegisterCustomCommandsRejectsInvalidConfig(t *testing.T) {
	a, _ := newTestApp()
	defer a.Close()

	cfg := &CustomCommandConfig{Commands: []CustomCommandDef{{Name: "x"}}} // no params/template
	if err := a.RegisterCustomCommands(cfg); err == nil {
		t.Fatal("expected error for invalid custom command config")
	}
}

func TestLoadCustomCommandConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "commands.json")
	body := `{"commands":[{"name":"english","params":["text"],"template":"$text"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadCustomCommandConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Commands) != 1 || cfg.Commands[0].Name != "english" {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestLoadCustomCommandConfigMissingFile(t *testing.T) {
	if _, err := LoadCustomCommandConfig("/no/such/file.json"); err == nil {
		t.Fatal("expected error for missing file")
	}
}
