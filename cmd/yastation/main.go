// Command yastation is the interactive REPL: connects to the station,
// then reads commands from stdin. Free text (no leading /) is spoken by
// the default speaker; everything else follows the /help table.
//
// It also supports one-shot use for scripting/cron/shell aliases,
// without dropping into the interactive prompt:
//
//	yastation "привет с компа"              # one free-text command, exits
//	yastation -c "/volume 0.3"              # one explicit command, exits
//	yastation -c "/volume 0.3" -c "/say привет"   # several, run in order, exits
//	yastation -e examples/morning.txt        # shorthand for -c "/execute ..."
//	yastation -c "/every 30m /say вода" -i    # schedule something, then stay in the REPL
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/denizsincar29/yastation/internal/app"
	"github.com/denizsincar29/yastation/internal/quasar"
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
	flag.Var(&commands, "c", "выполнить одну команду и продолжить (можно указывать несколько раз), например -c \"/say привет\"")
	flag.StringVar(&scriptPath, "e", "", "выполнить скрипт файлом (как /execute путь) и выйти")
	flag.BoolVar(&forceInteractive, "i", false, "остаться в интерактивном REPL после выполнения -c/-e")
	flag.StringVar(&configPath, "config", os.Getenv("YASTATION_COMMANDS_FILE"), "путь к JSON со своими командами (см. examples/commands.json); по умолчанию из YASTATION_COMMANDS_FILE")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "использование:")
		fmt.Fprintln(os.Stderr, "  yastation                          интерактивный REPL")
		fmt.Fprintln(os.Stderr, "  yastation \"текст\"                   сказать текст один раз и выйти")
		fmt.Fprintln(os.Stderr, "  yastation -c \"/volume 3\" -c \"/say привет\"   несколько команд подряд, потом выход")
		fmt.Fprintln(os.Stderr, "  yastation -e script.txt             выполнить скрипт и выйти")
		fmt.Fprintln(os.Stderr, "  yastation -c \"...\" -i               выполнить и остаться в REPL")
		fmt.Fprintln(os.Stderr, "  yastation -config commands.json     подключить свои команды")
		flag.PrintDefaults()
	}
	flag.Parse()

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
	repl(ctx, a)
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

func repl(ctx context.Context, a *app.App) {
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
