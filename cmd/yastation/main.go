// Command yastation is the interactive REPL: connects to the station,
// then reads commands from stdin. Free text (no leading /) is spoken by
// the default speaker; everything else follows the /help table.
//
// It also supports one-shot use for scripting/cron/shell aliases,
// without dropping into the interactive prompt:
//
//	yastation "привет с компа"              # one free-text command, exits
//	yastation -c "volume 0.3"               # one explicit command, exits
//	yastation -c "volume 0.3" -c "say привет"   # several, run in order, exits
//	yastation -e examples/morning.txt        # shorthand for -c "execute ..."
//	yastation -c "every 30m /say вода" -i    # schedule something, then stay in the REPL
//
// -c's value is "command [args...]" — the leading "/" of the command
// name itself is added automatically if missing (and left alone if you
// do type it), because Git Bash rewrites an argument that starts with a
// literal "/" into a Windows path before Go ever sees it (that's the
// "-c" value as a whole, not anything embedded further inside it —
// /every's own nested "/say вода" argument above still needs its own
// "/", since that's parsed by /every's own handler afterward, not by
// this auto-slash step).
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/chzyer/readline"
	"github.com/denizsincar29/yastation/internal/app"
	"github.com/denizsincar29/yastation/internal/quasar"
	"github.com/denizsincar29/yastation/internal/sounds"
)

// stringList collects repeated -c flags in the order they were given.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, "; ") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	var commands stringList
	var scriptPath string
	var forceInteractive bool
	var configPath string
	var noReadline bool
	flag.Var(&commands, "c", "выполнить одну команду и продолжить (можно указывать несколько раз), например -c \"volume 0.3\"")
	flag.StringVar(&scriptPath, "e", "", "выполнить скрипт файлом (как /execute путь) и выйти")
	flag.BoolVar(&forceInteractive, "i", false, "остаться в интерактивном REPL после выполнения -c/-e")
	flag.StringVar(&configPath, "config", os.Getenv("YASTATION_COMMANDS_FILE"), "путь к JSON со своими командами (см. examples/commands.json); по умолчанию из YASTATION_COMMANDS_FILE")
	flag.BoolVar(&noReadline, "noreadline", os.Getenv("YASTATION_NO_READLINE") != "", "обычный построчный ввод вместо readline (без истории/tab) — на случай если readline глючит под твоим терминалом")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "использование:")
		fmt.Fprintln(os.Stderr, "  yastation                          интерактивный REPL")
		fmt.Fprintln(os.Stderr, "  yastation \"текст\"                   сказать текст один раз и выйти")
		fmt.Fprintln(os.Stderr, "  yastation -c \"volume 3\" -c \"say привет\"   несколько команд подряд, потом выход")
		fmt.Fprintln(os.Stderr, "  yastation -e script.txt             выполнить скрипт и выйти")
		fmt.Fprintln(os.Stderr, "  yastation -c \"...\" -i               выполнить и остаться в REPL")
		fmt.Fprintln(os.Stderr, "  yastation -config commands.json     подключить свои команды")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  /команда?                          справка по одной команде (например /timer?)")
		fmt.Fprintln(os.Stderr, "  встроенные шаблонные команды (play/pause/timer/...) читаются из")
		fmt.Fprintln(os.Stderr, "  "+app.ConfigFilePath()+" — при первом запуске файл создаётся из config.json.default,")
		fmt.Fprintln(os.Stderr, "  дальше можно редактировать/удалять команды прямо там")
		flag.PrintDefaults()
	}
	flag.Parse()

	// -c values are commands, same grammar as the REPL — but unlike the
	// REPL, a leading "/" typed on an actual command line gets eaten by
	// Git Bash's own argv mangling before Go ever sees it: it rewrites
	// anything that looks like an absolute Unix path, so
	// `yastation -c "/pause"` arrives here as
	// `yastation -c "C:\Program Files\Git\pause"`. So -c treats its
	// value as "command name [args...]" and adds the "/" back itself if
	// it's missing — write `-c "pause"`, not `-c "/pause"`, and it works
	// the same either way, on any shell. Positional free text (no -c,
	// e.g. `yastation "привет с компа"`) stays untouched — that's meant
	// to be spoken, not run as a command, so there's no slash to add.
	normalizeCFlagCommands(commands)

	// A single bare positional argument with no flags is shorthand for
	// one -c: `yastation "привет с компа"`.
	if positional := flag.Args(); len(positional) > 0 {
		commands = append(commands, strings.Join(positional, " "))
	}
	if scriptPath != "" {
		commands = append(commands, "/execute "+scriptPath)
	}

	fmt.Println("Подключаюсь к Яндекс Станции...")
	client, err := quasar.Connect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Не удалось подключиться:", err)
		fmt.Fprintln(os.Stderr, "Если это первый запуск — авторизуйтесь: go run ./cmd/yastation-auth")
		os.Exit(1)
	}
	fmt.Printf("Подключено. Колонок найдено: %d (%s)\n", len(client.Speakers), speakerNames(client.Speakers))

	a := app.New(client)
	defer a.Close()

	defaultsPath := app.ConfigFilePath()
	if err := app.EnsureConfigFile(defaultsPath); err != nil {
		fmt.Fprintln(os.Stderr, "Не смог создать", defaultsPath, ":", err)
		os.Exit(1)
	}
	defaultsCfg, err := app.LoadCustomCommandConfig(defaultsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Не смог загрузить", defaultsPath, ":", err)
		os.Exit(1)
	}
	if err := a.RegisterCustomCommands(defaultsCfg); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка в", defaultsPath, ":", err)
		os.Exit(1)
	}
	fmt.Printf("Загружено стандартных команд: %d (из %s)\n", len(defaultsCfg.Commands), defaultsPath)

	if configPath != "" {
		cfg, err := app.LoadCustomCommandConfig(configPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Не смог загрузить свои команды:", err)
			os.Exit(1)
		}
		if err := a.RegisterCustomCommands(cfg); err != nil {
			fmt.Fprintln(os.Stderr, "Ошибка в конфиге команд:", err)
			os.Exit(1)
		}
		fmt.Printf("Загружено своих команд: %d (из %s)\n", len(cfg.Commands), configPath)
	}

	ctx := context.Background()

	if len(commands) > 0 {
		exitCode := runOnce(ctx, a, commands)
		if !forceInteractive {
			os.Exit(exitCode)
		}
	}

	fmt.Println("Пиши текст — он будет озвучен станцией. Команды — с /, /help — список.")
	if noReadline {
		replPlain(ctx, a)
	} else {
		repl(ctx, a, client)
	}
}

// normalizeCFlagCommands prepends "/" to every -c value that's missing
// one — see the -c handling comment in main for why (Git Bash argv
// mangling). Modifies commands in place.
func normalizeCFlagCommands(commands []string) {
	for i, c := range commands {
		if !strings.HasPrefix(c, "/") {
			commands[i] = "/" + c
		}
	}
}

// runOnce executes each command line in order, printing output/errors as
// it goes, and returns a process exit code (1 if anything failed, so
// shell scripts/cron jobs can detect it).
func runOnce(ctx context.Context, a *app.App, commands []string) int {
	exitCode := 0
	for _, line := range commands {
		out, err := a.Execute(ctx, line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s -> ошибка: %v\n", line, err)
			exitCode = 1
			continue
		}
		if out != "" {
			fmt.Println(out)
		}
	}
	return exitCode
}

func speakerNames(speakers []quasar.Device) string {
	names := make([]string, len(speakers))
	for i, d := range speakers {
		names[i] = d.Name
	}
	return strings.Join(names, ", ")
}

// repl reads commands interactively with readline-style editing: history
// navigable via the up/down arrows (like Python's input() gets for free
// from GNU readline), left/right/Home/End/Backspace, Ctrl+C to cancel
// the current line (or quit if the line is already empty — same as most
// shells), Ctrl+D/EOF to quit. History is in-memory for the session
// only — nothing is written to disk.
//
// If stdin isn't a real terminal (piped input, some non-standard
// terminal readline can't drive), it falls back to plain line-by-line
// reading with no history — same as before this feature existed, so
// nothing regresses for scripted/non-interactive use.
func repl(ctx context.Context, a *app.App, client *quasar.Client) {
	rl, err := readline.NewEx(&readline.Config{
		Prompt:                 "> ",
		DisableAutoSaveHistory: true, // we save manually below, skipping blanks/immediate repeats
		AutoComplete:           newCompleter(a, client),
	})
	if err != nil {
		replPlain(ctx, a)
		return
	}
	defer rl.Close()

	var lastLine string
	for {
		result := rl.Line()
		if result.CanContinue() {
			// Ctrl+C with text already typed — shell-like behaviour:
			// discard that line, stay in the REPL, fresh prompt.
			continue
		}
		if result.CanBreak() {
			// Ctrl+C on an *empty* line, or Ctrl+D/EOF — actually quit.
			// (The previous version continued unconditionally on any
			// interrupt, which is why Ctrl+C looked like it "did
			// nothing" — there was no way to ever exit with it.)
			break
		}
		line := result.Line
		if line == "" {
			continue
		}
		if line != lastLine {
			rl.SaveHistory(line)
			lastLine = line
		}
		out, err := a.Execute(ctx, line)
		if err != nil {
			fmt.Println("Ошибка:", err)
		} else if out != "" {
			fmt.Println(out)
		}
	}
	fmt.Println("\nОтключено.")
}

// replPlain is the original bufio-based reader, kept as a fallback for
// when readline can't take over the terminal (e.g. piped/redirected
// stdin) — no arrow-key history there, but that was never expected to
// work over a pipe anyway.
func replPlain(ctx context.Context, a *app.App) {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Print("> ")
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			fmt.Print("> ")
			continue
		}
		out, err := a.Execute(ctx, line)
		if err != nil {
			fmt.Println("Ошибка:", err)
		} else if out != "" {
			fmt.Println(out)
		}
		fmt.Print("> ")
	}
	fmt.Println("\nОтключено.")
}

// completer implements readline.AutoCompleter. Tab completion covers:
//   - the command itself, at the start of the line ("/sou<TAB>" -> "/sound", "/sounds", "/soundlist", "/soundcategories", "/sndcat")
//   - sound_play ids as the last argument to /sound, /sounds or /soundlist
//     (their aliases included) — e.g. "/sound coug<TAB>" -> "/sound cough-1"/"cough-2"
//   - inside an unclosed "[query" anywhere on the line — the speaker_audio
//     catalog (id or RU name), the one [query] embeds via /say or bare
//     text — regardless of what command (if any) the line starts with,
//     since [sound] markup works the same inside /say and inside plain
//     text with no leading "/" at all
//   - a real speaker name right after "station=", for any command —
//     e.g. "/volume station=Кух<TAB>" -> the matching device name(s)
//
// It intentionally does NOT try to complete every command's own free-text
// arguments (song names, timer durations, ...) — those aren't from a
// closed set, so there's nothing useful to offer.
type completer struct {
	commands     []string // every registered name/alias, with the leading "/"
	soundIDs     []string
	speakerAudio []string // speaker_audio catalog: both full ids and RU names, flat
	stations     []string
}

// soundArgCommands are the command names (without the leading "/",
// canonical name or alias — any of them) whose last argument is a
// sound_play id/query, so tab-completing against the sound catalog makes
// sense there.
var soundArgCommands = map[string]bool{
	"sound":     true,
	"sounds":    true,
	"soundlist": true,
}

func newCompleter(a *app.App, client *quasar.Client) *completer {
	seen := map[string]bool{}
	var commands []string
	for _, n := range a.Dispatcher.Names() {
		full := "/" + n
		if !seen[full] {
			seen[full] = true
			commands = append(commands, full)
		}
	}
	sort.Strings(commands)

	var soundIDs []string
	for _, e := range sounds.Effects() {
		soundIDs = append(soundIDs, e.ID)
	}
	sort.Strings(soundIDs)

	var speakerAudio []string
	for _, s := range sounds.SpeakerAudios() {
		speakerAudio = append(speakerAudio, s.FullID, s.NameRU)
	}
	sort.Strings(speakerAudio)

	var stations []string
	for _, d := range client.Speakers {
		stations = append(stations, d.Name)
	}
	sort.Strings(stations)

	return &completer{commands: commands, soundIDs: soundIDs, speakerAudio: speakerAudio, stations: stations}
}

// Do implements readline.AutoCompleter. line is the whole buffer, pos is
// the cursor offset into it (in runes) — only the part before the cursor
// is used to figure out what's being completed. Return value: candidate
// suffixes to append (not full replacements) and how many trailing runes
// of the already-typed word they replace/extend.
func (c *completer) Do(line []rune, pos int) (newLine [][]rune, length int) {
	text := string(line[:pos])

	// Inside an unclosed "[query" — complete against the [sound] markup
	// catalog no matter what command (if any) started the line, since
	// "[" isn't a word boundary for the usual space-splitting below and
	// this markup works identically inside /say and bare text.
	if openIdx, ok := lastUnclosedBracket(text); ok {
		return completeAgainst(c.speakerAudio, text[openIdx+1:])
	}

	wordStart := strings.LastIndexAny(text, " \t")
	word := text[wordStart+1:]
	typedBefore := strings.Fields(text[:wordStart+1])

	switch {
	case len(typedBefore) == 0 && strings.HasPrefix(word, "/"):
		return completeAgainst(c.commands, word)

	case strings.HasPrefix(word, "station="):
		val := strings.TrimPrefix(word, "station=")
		cands, n := completeAgainst(c.stations, val)
		return cands, n // length is relative to val, not the "station=" prefix — correct as-is

	case len(typedBefore) >= 1:
		cmd := strings.TrimPrefix(typedBefore[0], "/")
		if soundArgCommands[cmd] {
			return completeAgainst(c.soundIDs, word)
		}
	}
	return nil, 0
}

// lastUnclosedBracket reports the index of the last "[" in text that
// has no matching "]" after it — i.e. the cursor sits inside an open
// "[query" marker (not yet a complete "[query]"). ok is false if there
// isn't one (no "[" at all, or the last one is already closed before
// the cursor).
func lastUnclosedBracket(text string) (idx int, ok bool) {
	openIdx := strings.LastIndexByte(text, '[')
	if openIdx == -1 {
		return 0, false
	}
	if strings.IndexByte(text[openIdx:], ']') != -1 {
		return 0, false
	}
	return openIdx, true
}

// completeAgainst returns every option in options that starts with word
// (case-insensitively), as the runes remaining after word, plus len(word)
// in runes — matching what readline.AutoCompleter.Do expects.
func completeAgainst(options []string, word string) (newLine [][]rune, length int) {
	lw := strings.ToLower(word)
	wordLen := len([]rune(word))
	var out [][]rune
	for _, o := range options {
		if strings.HasPrefix(strings.ToLower(o), lw) {
			out = append(out, []rune(o)[wordLen:])
		}
	}
	return out, wordLen
}
