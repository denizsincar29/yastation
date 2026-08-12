package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// CustomCommandDef describes one user-defined command, e.g.
//
//	{
//	  "name": "english",
//	  "aliases": ["en"],
//	  "help": "Сказать фразу по-английски: /english текст",
//	  "params": ["text"],
//	  "template": "Say this exact words in English: \"$text\"",
//	  "kind": "command",
//	  "category": "Свои команды"
//	}
//
// All params except the last must be a single word; the last one greedily
// captures the rest of the line (so "text" above can contain spaces).
// A param name ending in "?" (e.g. "label?") is optional — it and every
// param after it may be left unset, and empty params render as "" in the
// template. Only trailing params may be optional. params may be empty
// entirely, for a fixed no-argument command (e.g. "params": [],
// "template": "продолжить").
//
// Template placeholders are "$paramName", substituted from the bound
// params (surrounding whitespace left over from an unset optional param
// is trimmed). Kind is "command" (default — sent as if spoken to Alice,
// like /cmd) or "say" (sent as flat TTS, like /say). Category groups the
// command in /help; defaults to "Свои команды" if omitted — this is what
// distinguishes a user's own commands.json from yastation's own built-in
// config.json (see config.json.default), which sets an explicit category
// per command ("Плеер", "Напоминания", ...).
type CustomCommandDef struct {
	Name     string   `json:"name"`
	Aliases  []string `json:"aliases,omitempty"`
	Help     string   `json:"help,omitempty"`
	Params   []string `json:"params"`
	Template string   `json:"template"`
	Kind     string   `json:"kind,omitempty"`
	Category string   `json:"category,omitempty"`
}

// CustomCommandConfig is the top-level shape of the JSON config file.
type CustomCommandConfig struct {
	Commands []CustomCommandDef `json:"commands"`
}

// LoadCustomCommandConfig reads and parses a custom-commands JSON file.
// See examples/commands.json for a worked example.
func LoadCustomCommandConfig(path string) (*CustomCommandConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("не смог прочитать %s: %w", path, err)
	}
	var cfg CustomCommandConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("не смог разобрать %s: %w", path, err)
	}
	return &cfg, nil
}

// RegisterCustomCommands validates and registers every command in cfg on
// this App's dispatcher. Returns an error (without registering anything
// further) on the first invalid definition, naming which one and why.
func (a *App) RegisterCustomCommands(cfg *CustomCommandConfig) error {
	for _, def := range cfg.Commands {
		if err := validateCustomCommandDef(def); err != nil {
			return fmt.Errorf("команда %q: %w", def.Name, err)
		}
	}
	for _, def := range cfg.Commands {
		a.registerCustomCommand(def)
	}
	return nil
}

func validateCustomCommandDef(def CustomCommandDef) error {
	if def.Name == "" {
		return fmt.Errorf("нужно имя (name)")
	}
	if def.Template == "" {
		return fmt.Errorf("нужен шаблон (template)")
	}
	if def.Kind != "" && def.Kind != "command" && def.Kind != "say" {
		return fmt.Errorf(`kind должен быть "command" или "say", получено %q`, def.Kind)
	}
	seen := map[string]bool{}
	sawOptional := false
	for _, p := range def.Params {
		if p == "" || p == "?" {
			return fmt.Errorf("пустое имя параметра")
		}
		name := strings.TrimSuffix(p, "?")
		if seen[name] {
			return fmt.Errorf("повторяющийся параметр %q", name)
		}
		seen[name] = true
		if strings.HasSuffix(p, "?") {
			sawOptional = true
		} else if sawOptional {
			return fmt.Errorf("обязательный параметр %q идёт после необязательного — необязательные (с ?) должны быть последними", name)
		}
	}
	return nil
}

func (a *App) registerCustomCommand(def CustomCommandDef) {
	names := append([]string{def.Name}, def.Aliases...)
	help := def.Help
	if help == "" {
		params := strings.TrimSuffix(strings.Join(def.Params, " "), "?")
		help = strings.TrimSpace("Своя команда: /" + def.Name + " " + params)
	}
	category := def.Category
	if category == "" {
		category = "Свои команды"
	}

	a.Dispatcher.HandleCat(category, help, func(ctx context.Context, args []string) (string, error) {
		st, rest := station(args)
		values, err := bindCustomParams(def.Params, rest)
		if err != nil {
			return "", err
		}
		phrase := strings.TrimSpace(renderCustomTemplate(def.Template, values))

		var sendErr error
		if def.Kind == "say" {
			sendErr = a.Client.Say(st, phrase)
		} else {
			sendErr = a.Client.Command(st, phrase)
		}
		if sendErr != nil {
			return "", sendErr
		}
		return fmt.Sprintf("[%s] %s", def.Name, phrase), nil
	}, names...)
}

// bindCustomParams binds args positionally to params. Every param except
// the last takes exactly one word; the last one takes everything left
// (joined with spaces), so a final free-text argument can contain spaces.
// A param name ending in "?" is optional (validateCustomCommandDef
// guarantees only trailing params carry it) — if there aren't enough args
// to reach it, it's bound to "" instead of erroring.
func bindCustomParams(params, args []string) (map[string]string, error) {
	var required []string
	for _, p := range params {
		if !strings.HasSuffix(p, "?") {
			required = append(required, p)
		}
	}

	values := make(map[string]string, len(params))
	for i, raw := range params {
		name := strings.TrimSuffix(raw, "?")
		optional := strings.HasSuffix(raw, "?")
		last := i == len(params)-1

		switch {
		case i >= len(args):
			if !optional {
				return nil, fmt.Errorf("нужно параметров: %d (%s), дано: %d", len(required), strings.Join(required, ", "), len(args))
			}
			values[name] = ""
		case last:
			values[name] = strings.Join(args[i:], " ")
		default:
			values[name] = args[i]
		}
	}
	return values, nil
}

var customPlaceholderRe = regexp.MustCompile(`\$(\w+)`)

// renderCustomTemplate replaces every "$paramName" in tmpl with its bound
// value. Unknown placeholders (typo'd param names) are left as-is rather
// than silently dropped, so a bad config is easy to notice in the output.
func renderCustomTemplate(tmpl string, values map[string]string) string {
	return customPlaceholderRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		name := m[1:]
		if v, ok := values[name]; ok {
			return v
		}
		return m
	})
}
