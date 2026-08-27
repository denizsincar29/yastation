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
// This is the REPL/slash-line shape — see BoundHandler for the shape
// HTTP JSON bodies and MCP tool calls use instead.
type Handler func(ctx context.Context, args []string) (string, error)

// Param describes one named argument a bound command handler accepts —
// the shape HTTP JSON request bodies and MCP tool schemas are generated
// from (see cmd/yastation-server's handleCommandByName and mcp.go).
type Param struct {
	Name     string // JSON field / MCP tool property name
	Kind     string // "string" or "number" — schema/doc hint only; values in BoundHandler's map are always strings
	Optional bool
	Help     string
}

// BoundHandler is a command implementation that receives its arguments
// already named and separated — station picked out on its own, every
// other declared Param available by name in values — instead of a raw
// token slice. This is the natural shape of an HTTP JSON body or an MCP
// tool call, so it's what CallNamed invokes directly: no slash-line is
// ever built or parsed on that path. Slash-line tokenizing (tokenize,
// Execute) stays a REPL-only concern, upstream of BoundHandler — see
// HandleBoundCat, which derives the REPL entry point from a BoundHandler
// automatically.
type BoundHandler func(ctx context.Context, station string, values map[string]string) (string, error)

type command struct {
	name         string
	category     string
	help         string
	handler      Handler
	params       []Param
	takesStation bool
	bound        BoundHandler
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
	d.register(&command{category: category, help: help, handler: handler}, names)
}

// HandleNamed registers a command with two independent entry points:
// legacy (raw positional args — reached only through the REPL/slash-line
// path, Execute/ExecuteArgs) and bound (named values — reached only
// through CallNamed, which is what HTTP JSON bodies and MCP tool calls
// use). params documents bound's shape for HTTP/MCP schema generation;
// takesStation says whether this command accepts a target speaker.
//
// Most commands' legacy and bound behaviour is identical modulo how the
// arguments arrive — for those, register with HandleBoundCat instead, which
// derives legacy from bound automatically via plain positional binding.
// Reach for HandleNamed directly only when the REPL syntax has its own
// convention a plain positional bind can't reproduce (e.g. /notify's
// "volume=" keyword-anywhere-in-the-args style).
func (d *Dispatcher) HandleNamed(category, help string, takesStation bool, params []Param, legacy Handler, bound BoundHandler, names ...string) {
	d.register(&command{category: category, help: help, handler: legacy, params: params, takesStation: takesStation, bound: bound}, names)
}

// HandleBoundCat registers a command whose REPL/slash-line behaviour is
// plain positional binding of params — station=, if takesStation, then
// each param in declared order (every param but the last a single
// token, the last one greedy over whatever's left, see BindPositional) —
// derived automatically from bound, which is also what CallNamed invokes
// directly for HTTP/MCP callers. This covers the large majority of
// commands; see HandleNamed for ones with a REPL-only argument
// convention that doesn't fit this pattern.
func (d *Dispatcher) HandleBoundCat(category, help string, takesStation bool, params []Param, bound BoundHandler, names ...string) {
	legacy := func(ctx context.Context, args []string) (string, error) {
		st := ""
		rest := args
		if takesStation {
			st, rest = extractStation(args)
		}
		values, err := BindPositional(params, rest)
		if err != nil {
			return "", err
		}
		return bound(ctx, st, values)
	}
	d.HandleNamed(category, help, takesStation, params, legacy, bound, names...)
}

// register is the shared bookkeeping behind Handle/HandleCat/HandleNamed:
// fills in cmd.name from the first name, files the category into catOrder
// the first time it's seen, and indexes cmd under every given name/alias.
func (d *Dispatcher) register(cmd *command, names []string) {
	if len(names) == 0 {
		panic("dispatch: at least one name required")
	}
	if cmd.category == "" {
		cmd.category = uncategorized
	}
	cmd.name = names[0]
	d.order = append(d.order, cmd)
	if !d.catSeen[cmd.category] {
		d.catSeen[cmd.category] = true
		d.catOrder = append(d.catOrder, cmd.category)
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

// CallNamed runs a command using already-named, already-typed values
// directly — no slash-line is built or parsed on this path at all. This
// is the entry point HTTP JSON request bodies and MCP tool calls use
// (see cmd/yastation-server). ok is false if name isn't registered, or
// was registered via plain Handle/HandleCat with no bound handler (a
// REPL-only command with no named-parameter shape to call this way).
func (d *Dispatcher) CallNamed(ctx context.Context, name, station string, values map[string]string) (out string, ok bool, err error) {
	cmd, exists := d.byName[name]
	if !exists || cmd.bound == nil {
		return "", false, nil
	}
	out, err = cmd.bound(ctx, station, values)
	return out, true, err
}

// CommandSpec describes one bound command's shape — everything
// cmd/yastation-server needs to generate a POST /commands/{name} JSON
// body schema or an MCP tool's input schema without hardcoding anything
// per-command.
type CommandSpec struct {
	Name         string
	Category     string
	Help         string
	TakesStation bool
	Params       []Param
}

// Spec returns name's CommandSpec (by canonical name or any alias). ok
// is false if name isn't registered or has no bound handler.
func (d *Dispatcher) Spec(name string) (CommandSpec, bool) {
	cmd, ok := d.byName[name]
	if !ok || cmd.bound == nil {
		return CommandSpec{}, false
	}
	return CommandSpec{Name: cmd.name, Category: cmd.category, Help: cmd.help, TakesStation: cmd.takesStation, Params: cmd.params}, true
}

// Specs returns every bound command's CommandSpec, one entry per command
// with aliases folded in, sorted by canonical name.
func (d *Dispatcher) Specs() []CommandSpec {
	seen := map[*command]bool{}
	var out []CommandSpec
	for _, cmd := range d.order {
		if seen[cmd] || cmd.bound == nil {
			continue
		}
		seen[cmd] = true
		out = append(out, CommandSpec{Name: cmd.name, Category: cmd.category, Help: cmd.help, TakesStation: cmd.takesStation, Params: cmd.params})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
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

// extractStation pulls the REPL-only "station=Name" convention out of a
// raw arg list — anywhere in the list, not just first/last — used by
// HandleBoundCat's derived legacy handler to feed the right station into
// a BoundHandler. HTTP/MCP callers never go through this: they already
// have station as its own named value by the time CallNamed is called.
func extractStation(args []string) (string, []string) {
	for i, a := range args {
		if strings.HasPrefix(a, "station=") {
			name := strings.TrimPrefix(a, "station=")
			rest := append(append([]string{}, args[:i]...), args[i+1:]...)
			return name, rest
		}
	}
	return "", args
}

// BindPositional binds a REPL arg list positionally to params, in
// order: every param but the last takes exactly one token; the last one
// takes everything left (space-joined), so a trailing free-text param
// can contain spaces — same convention internal/app's custom-command
// templates have always used. A param with Optional=true may be left
// unreached if there aren't enough args (comes out as ""); the caller is
// responsible for only marking trailing params Optional, same as
// internal/app.validateCustomCommandDef already enforces at load time.
func BindPositional(params []Param, args []string) (map[string]string, error) {
	var required []string
	for _, p := range params {
		if !p.Optional {
			required = append(required, p.Name)
		}
	}
	values := make(map[string]string, len(params))
	for i, p := range params {
		last := i == len(params)-1
		switch {
		case i >= len(args):
			if !p.Optional {
				return nil, fmt.Errorf("нужно параметров: %d (%s), дано: %d", len(required), strings.Join(required, ", "), len(args))
			}
			values[p.Name] = ""
		case last:
			values[p.Name] = strings.Join(args[i:], " ")
		default:
			values[p.Name] = args[i]
		}
	}
	return values, nil
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
