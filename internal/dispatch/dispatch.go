// Package dispatch is a small, hand-rolled command dispatcher — not a
// port of func_parser (there's no Go version of that yet, only Python and
// Rust). Just enough to support "/command arg1 arg2 rest of the words"
// with quoted arguments, a default (no "/") handler, aliases, and a
// registry you can list for /help. If a proper Go func_parser shows up
// later this package is small enough to swap out.
package dispatch

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Handler is one command's implementation. args is everything after the
// command name, already tokenized (quoted substrings kept together).
type Handler func(ctx context.Context, args []string) (string, error)

type command struct {
	name    string
	help    string
	handler Handler
}

// Dispatcher holds the command table plus an optional default handler for
// input that doesn't start with the prefix.
type Dispatcher struct {
	Prefix  string
	byName  map[string]*command
	order   []*command
	Default func(ctx context.Context, text string) (string, error)
}

// New creates a dispatcher using "/" as the command prefix.
func New() *Dispatcher {
	return &Dispatcher{Prefix: "/", byName: map[string]*command{}}
}

// Handle registers a command under name and any aliases, all sharing the
// same handler and help text.
func (d *Dispatcher) Handle(help string, handler Handler, names ...string) {
	if len(names) == 0 {
		panic("dispatch.Handle: at least one name required")
	}
	cmd := &command{name: names[0], help: help, handler: handler}
	d.order = append(d.order, cmd)
	for _, n := range names {
		d.byName[n] = cmd
	}
}

// Execute parses one line of input and runs the matching handler, or the
// default handler if the line doesn't start with the prefix.
func (d *Dispatcher) Execute(ctx context.Context, line string) (string, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", nil
	}
	if !strings.HasPrefix(line, d.Prefix) {
		if d.Default == nil {
			return "", fmt.Errorf("нет команды по умолчанию для свободного текста")
		}
		return d.Default(ctx, line)
	}

	tokens, err := tokenize(strings.TrimPrefix(line, d.Prefix))
	if err != nil {
		return "", err
	}
	if len(tokens) == 0 {
		return "", fmt.Errorf("пустая команда")
	}
	name, args := tokens[0], tokens[1:]
	cmd, ok := d.byName[name]
	if !ok {
		return "", fmt.Errorf("неизвестная команда: %s%s (наберите %shelp)", d.Prefix, name, d.Prefix)
	}
	return cmd.handler(ctx, args)
}

// Help renders a sorted, deduplicated list of registered commands.
func (d *Dispatcher) Help() string {
	seen := map[*command]bool{}
	var lines []string
	for _, cmd := range d.order {
		if seen[cmd] {
			continue
		}
		seen[cmd] = true
		lines = append(lines, fmt.Sprintf("  %s%-14s %s", d.Prefix, cmd.name, cmd.help))
	}
	sort.Strings(lines)
	return "Доступные команды:\n" + strings.Join(lines, "\n")
}

// Rest joins a token slice back into a single space-separated string —
// most of our handlers take "the rest of the line" as free text.
func Rest(args []string) string { return strings.Join(args, " ") }

// tokenize splits on whitespace but keeps "double quoted substrings" and
// 'single quoted substrings' together as one token.
func tokenize(s string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	inQuote := rune(0)
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case inQuote != 0:
			if r == inQuote {
				inQuote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			inQuote = r
		case r == ' ' || r == '\t':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	if inQuote != 0 {
		return nil, fmt.Errorf("незакрытая кавычка")
	}
	flush()
	return tokens, nil
}
