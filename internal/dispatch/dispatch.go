// Package dispatch is a small, hand-rolled command dispatcher — not a
// port of func_parser (there's no Go version of that yet, only Python and
// Rust). Just enough to support "/command arg1 arg2 rest of the words"
// with quoted arguments, a default (no "/") handler, aliases, categories,
// and a registry you can list for /help (as a whole, or one command at a
// time via "/command?"). If a proper Go func_parser shows up later this
// package is small enough to swap out.
package dispatch

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// uncategorized is the bucket name used when a command is registered via
// Handle (no category given) instead of HandleCat.
const uncategorized = "Прочее"

// Handler is one command's implementation. args is everything after the
// command name, already tokenized (quoted substrings kept together).
type Handler func(ctx context.Context, args []string) (string, error)

type command struct {
	name     string
	category string
	help     string
	handler  Handler
}

// Dispatcher holds the command table plus an optional default handler for
// input that doesn't start with the prefix.
type Dispatcher struct {
	Prefix   string
	byName   map[string]*command
	order    []*command
	catSeen  map[string]bool
	catOrder []string
	Default  func(ctx context.Context, text string) (string, error)
}

// New creates a dispatcher using "/" as the command prefix.
func New() *Dispatcher {
	return &Dispatcher{Prefix: "/", byName: map[string]*command{}, catSeen: map[string]bool{}}
}

// Handle registers a command under name and any aliases, all sharing the
// same handler and help text, filed under the catch-all "Прочее" category.
// Prefer HandleCat when the command has a natural home.
func (d *Dispatcher) Handle(help string, handler Handler, names ...string) {
	d.HandleCat(uncategorized, help, handler, names...)
}

// HandleCat is Handle plus an explicit category name, used to group
// /help output (e.g. "Плеер", "Напоминания") instead of dumping every
// command in one alphabetical list.
func (d *Dispatcher) HandleCat(category, help string, handler Handler, names ...string) {
	if len(names) == 0 {
		panic("dispatch.Handle: at least one name required")
	}
	if category == "" {
		category = uncategorized
	}
	cmd := &command{name: names[0], category: category, help: help, handler: handler}
	d.order = append(d.order, cmd)
	if !d.catSeen[category] {
		d.catSeen[category] = true
		d.catOrder = append(d.catOrder, category)
	}
	for _, n := range names {
		d.byName[n] = cmd
	}
}

// Execute parses one line of input and runs the matching handler, or the
// default handler if the line doesn't start with the prefix.
//
// A command name ending in "?" (e.g. "/cmd?") is special: instead of
// running the command, it returns that command's own help line, ignoring
// any further arguments. "/?" itself is unaffected — it's the existing
// alias for /help.
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

	if name != "?" && strings.HasSuffix(name, "?") {
		base := strings.TrimSuffix(name, "?")
		help, ok := d.HelpFor(base)
		if !ok {
			return "", fmt.Errorf("неизвестная команда: %s%s (наберите %shelp)", d.Prefix, base, d.Prefix)
		}
		return help, nil
	}

	cmd, ok := d.byName[name]
	if !ok {
		return "", fmt.Errorf("неизвестная команда: %s%s (наберите %shelp)", d.Prefix, name, d.Prefix)
	}
	return cmd.handler(ctx, args)
}

// ExecuteArgs runs the named command directly with already-split args
// (no "/prefix line" tokenizing/quoting round-trip) — for callers that
// already have a name and a []string, like yastation-server's
// per-command HTTP endpoints (see cmd/yastation-server).
func (d *Dispatcher) ExecuteArgs(ctx context.Context, name string, args []string) (string, error) {
	cmd, ok := d.byName[name]
	if !ok {
		return "", fmt.Errorf("неизвестная команда: %s%s (наберите %shelp)", d.Prefix, name, d.Prefix)
	}
	return cmd.handler(ctx, args)
}

// Names returns every registered command's canonical name plus its
// aliases in one flat list. Used by yastation-server to auto-register one
// HTTP endpoint per command.
func (d *Dispatcher) Names() []string {
	var names []string
	for n := range d.byName {
		names = append(names, n)
	}
	return names
}

// Help renders every registered command, grouped by category (in the
// order categories were first registered) and sorted alphabetically
// within each category.
func (d *Dispatcher) Help() string {
	seen := map[*command]bool{}
	byCat := map[string][]*command{}
	for _, cmd := range d.order {
		if seen[cmd] {
			continue
		}
		seen[cmd] = true
		byCat[cmd.category] = append(byCat[cmd.category], cmd)
	}

	var b strings.Builder
	b.WriteString("Доступные команды (подробнее про одну: /команда?):\n")
	for _, cat := range d.catOrder {
		cmds := byCat[cat]
		if len(cmds) == 0 {
			continue
		}
		sort.Slice(cmds, func(i, j int) bool { return cmds[i].name < cmds[j].name })
		b.WriteString("\n" + cat + ":\n")
		for _, cmd := range cmds {
			fmt.Fprintf(&b, "  %s%-14s %s\n", d.Prefix, cmd.name, cmd.help)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// HelpFor renders the help line for a single command by name or alias
// (e.g. "cmd", not "/cmd"), including its other aliases if it has any.
// Returns ok=false if no such command is registered.
func (d *Dispatcher) HelpFor(name string) (string, bool) {
	cmd, ok := d.byName[name]
	if !ok {
		return "", false
	}
	var aliases []string
	for n, c := range d.byName {
		if c == cmd && n != cmd.name {
			aliases = append(aliases, d.Prefix+n)
		}
	}
	sort.Strings(aliases)

	header := d.Prefix + cmd.name
	if len(aliases) > 0 {
		header += " (алиасы: " + strings.Join(aliases, ", ") + ")"
	}
	return header + "\n" + cmd.help, true
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
