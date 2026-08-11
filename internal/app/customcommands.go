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
//	  "kind": "command"
//	}
//
// All params except the last must be a single word; the last one greedily
// captures the rest of the line (so "text" above can contain spaces).
// Template placeholders are "$paramName", substituted from the bound
// params. Kind is "command" (default — sent as if spoken to Alice, like
// /cmd) or "say" (sent as flat TTS, like /say).
type CustomCommandDef struct {
	Name     string   `json:"name"`
	Aliases  []string `json:"aliases,omitempty"`
	Help     string   `json:"help,omitempty"`
	Params   []string `json:"params"`
	Template string   `json:"template"`
	Kind     string   `json:"kind,omitempty"`
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
	if len(def.Params) == 0 {
		return fmt.Errorf("нужен хотя бы один параметр (params)")
	}
	if def.Template == "" {
		return fmt.Errorf("нужен шаблон (template)")
	}
	if def.Kind != "" && def.Kind != "command" && def.Kind != "say" {
		return fmt.Errorf(`kind должен быть "command" или "say", получено %q`, def.Kind)
	}
	seen := map[string]bool{}
	for _, p := range def.Params {
		if p == "" {
			return fmt.Errorf("пустое имя параметра")
		}
		if seen[p] {
			return fmt.Errorf("повторяющийся параметр %q", p)
		}
		seen[p] = true
	}
	return nil
}

func (a *App) registerCustomCommand(def CustomCommandDef) {
	names := append([]string{def.Name}, def.Aliases...)
	help := def.Help
	if help == "" {
		help = "Своя команда: /" + def.Name + " " + strings.Join(def.Params, " ")
	}

	a.Dispatcher.Handle(help, func(ctx context.Context, args []string) (string, error) {
		st, rest := station(args)
		values, err := bindCustomParams(def.Params, rest)
		if err != nil {
			return "", err
		}
		phrase := renderCustomTemplate(def.Template, values)

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

// bindCustomParams binds args positionally to params: every param except
// the last takes exactly one word, the last one takes everything left
// (joined with spaces), so a final free-text argument can contain spaces.
func bindCustomParams(params, args []string) (map[string]string, error) {
	if len(args) < len(params) {
		return nil, fmt.Errorf("нужно параметров: %d (%s), дано: %d", len(params), strings.Join(params, ", "), len(args))
	}
	values := make(map[string]string, len(params))
	for i, p := range params {
		if i == len(params)-1 {
			values[p] = strings.Join(args[i:], " ")
		} else {
			values[p] = args[i]
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
