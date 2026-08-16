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

func TestRenderCustomTemplateSimpleConditionalTruthy(t *testing.T) {
	tmpl := "какая погода {{$city?в $city}}"

	got := renderCustomTemplate(tmpl, map[string]string{"city": "Москва"})
	if got != "какая погода в Москва" {
		t.Fatalf("got %q", got)
	}

	got = renderCustomTemplate(tmpl, map[string]string{"city": ""})
	if got != "какая погода " { // caller (registerCustomCommand) is the one that trims the outer edges
		t.Fatalf("got %q", got)
	}
}

func TestRenderCustomTemplateSwitchWithDefault(t *testing.T) {
	tmpl := "спроси мастер скилл прочитать сообщения {{$source==ls?в первых личных;$source==channel?на моём канале;$?первые 5 личных}}"

	cases := []struct {
		source string
		want   string
	}{
		{"ls", "спроси мастер скилл прочитать сообщения в первых личных"},
		{"channel", "спроси мастер скилл прочитать сообщения на моём канале"},
		{"", "спроси мастер скилл прочитать сообщения первые 5 личных"},
		{"anything-else", "спроси мастер скилл прочитать сообщения первые 5 личных"},
	}
	for _, c := range cases {
		got := renderCustomTemplate(tmpl, map[string]string{"source": c.source})
		if got != c.want {
			t.Fatalf("source=%q: got %q want %q", c.source, got, c.want)
		}
	}
}

func TestRenderCustomTemplateSingleEqualsAlsoWorksAsEquality(t *testing.T) {
	tmpl := "{{$mode=loud?ГРОМКО;$?тихо}}"
	if got := renderCustomTemplate(tmpl, map[string]string{"mode": "loud"}); got != "ГРОМКО" {
		t.Fatalf("got %q", got)
	}
	if got := renderCustomTemplate(tmpl, map[string]string{"mode": "quiet"}); got != "тихо" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderCustomTemplateNoDefaultAndNoMatchRendersEmpty(t *testing.T) {
	tmpl := "x{{$mode==loud?ГРОМКО}}y"
	got := renderCustomTemplate(tmpl, map[string]string{"mode": "quiet"})
	if got != "xy" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderCustomTemplateConditionalUnknownParamNeverMatches(t *testing.T) {
	tmpl := "{{$typo?should not appear;$?fallback}}"
	got := renderCustomTemplate(tmpl, map[string]string{"other": "x"})
	if got != "fallback" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderCustomTemplateConditionalBranchTextItselfHasPlaceholder(t *testing.T) {
	// the text half of a branch can reference *other* params too, not
	// just the one being tested in the condition
	tmpl := "{{$mode==loud?$name, ГРОМКО!;$?тихо, $name}}"
	got := renderCustomTemplate(tmpl, map[string]string{"mode": "loud", "name": "Дениз"})
	if got != "Дениз, ГРОМКО!" {
		t.Fatalf("got %q", got)
	}
	got = renderCustomTemplate(tmpl, map[string]string{"mode": "quiet", "name": "Дениз"})
	if got != "тихо, Дениз" {
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
