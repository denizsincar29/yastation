// Package app wires together the quasar client, the async command queue,
// the scheduler, and the command dispatcher into one reusable object.
// Both cmd/yastation (the REPL) and cmd/yastation-server (the HTTP
// backend) are thin wrappers around this package so behaviour stays
// identical between them.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/denizsincar29/yastation/internal/article"
	"github.com/denizsincar29/yastation/internal/dispatch"
	"github.com/denizsincar29/yastation/internal/quasar"
	"github.com/denizsincar29/yastation/internal/queue"
	"github.com/denizsincar29/yastation/internal/scheduler"
	"github.com/denizsincar29/yastation/internal/sounds"
)

// StationAPI is the subset of *quasar.Client that the command layer
// needs. Defined as an interface here so tests can use a fake instead of
// hitting the real Yandex API.
// Play, Pause, Timer, Weather, Reminder and friends used to live here too,
// each as its own typed method. In practice every one of them just built
// a fixed Russian phrase ("продолжить", "поставь таймер на N минут ...")
// and pushed it through Command — there was no protocol-level reason for
// them to be Go methods instead of data. They're now template-based
// commands loaded from config.json (see config.json.default and
// DefaultCommandsConfig) the same way a user's own --config commands.json
// is, and are registered through Command/Say below like any other custom
// command. Volume stays a real method: /volume and /notify need the
// numeric parsing/clamping that a plain text substitution can't do.
type StationAPI interface {
	Say(station, text string) error
	Command(station, text string) error
	Notify(station, text string, volume float64) error
	Volume(station string, level float64) error
	Batch(station string, actions []quasar.BatchAction) error
	RunScenario(name string) error
	ListScenarios() []string
	Diagnostics() (string, error)

	// Capabilities/RawCapability are the experimental, unverified escape
	// hatch into whatever Yandex's device capability list offers beyond
	// tts/text_action/volume — see quasar.Client for the caveats.
	Capabilities(station string) ([]any, error)
	RawCapability(station, capType, instance string, value any) error

	// The following were confirmed against a real device's Capabilities()
	// dump (see PROTOCOL_NOTES.md) rather than guessed — see quasar.Client
	// for exactly what's confirmed vs inferred by symmetry.
	SayWhisper(station, text string) error
	PlaySound(station, soundID, soundName string) error
	StopEverything(station string) error
	LightScene(station, sceneID string) error
	Weather(station string) error
	PlayMusic(station string) error

	// Refresh re-pulls devices/scenarios/capabilities from Yandex —
	// mainly useful to confirm a capability change (e.g. LightScene)
	// actually took effect, by reading Capabilities() again afterwards.
	Refresh() error
}

// App bundles a connected station client with the plumbing to run
// commands, either instantly (Execute) or fire-and-forget through the
// queue (Enqueue) so a slow ~1-2s Yandex round trip never blocks the
// caller.
type App struct {
	Client     StationAPI
	Queue      *queue.Queue
	Scheduler  *scheduler.Scheduler
	Dispatcher *dispatch.Dispatcher

	// FetchArticle retrieves a page's readable text for /read. Exposed so
	// tests can point it at a stub; defaults to article.Fetch.
	FetchArticle func(ctx context.Context, url string) (*article.Article, error)
}

// New builds an App around an already-connected station client
// (typically a *quasar.Client, or a fake in tests).
func New(client StationAPI) *App {
	a := &App{Client: client, FetchArticle: article.Fetch}
	a.Queue = queue.New(100, nil)
	a.Scheduler = scheduler.New(func(commandLine string) {
		a.Enqueue(commandLine)
	})
	a.Dispatcher = dispatch.New()
	a.registerCommands()
	return a
}

// Enqueue schedules commandLine to run in the background and returns
// immediately; use this for interactive/fire-and-forget callers (REPL,
// scheduled tasks).
func (a *App) Enqueue(commandLine string) {
	a.Queue.Enqueue(queue.Job{
		Label: commandLine,
		Run: func() error {
			_, err := a.Dispatcher.Execute(context.Background(), commandLine)
			return err
		},
	})
}

// Execute runs commandLine and waits for the actual result — used by the
// HTTP backend, which needs to answer the caller with success/failure,
// while still funnelling through the same single-worker queue so
// concurrent HTTP requests don't race each other editing the same
// speaker's scenario.
func (a *App) Execute(ctx context.Context, commandLine string) (string, error) {
	var out string
	err := a.Queue.EnqueueWait(ctx, queue.Job{
		Label: commandLine,
		Run: func() error {
			var runErr error
			out, runErr = a.Dispatcher.Execute(ctx, commandLine)
			return runErr
		},
	})
	return out, err
}

// ExecuteArgs runs the named command directly with already-split args,
// through the same single-worker queue as Execute (so it can't race a
// concurrent /command call touching the same speaker's scenario).
func (a *App) ExecuteArgs(ctx context.Context, name string, args []string) (string, error) {
	var out string
	err := a.Queue.EnqueueWait(ctx, queue.Job{
		Label: "/" + name,
		Run: func() error {
			var runErr error
			out, runErr = a.Dispatcher.ExecuteArgs(ctx, name, args)
			return runErr
		},
	})
	return out, err
}

// ExecuteNamed runs the named command with already-named values (station
// separate, everything else in values by dispatch.Param.Name), through
// the same single-worker queue as Execute/ExecuteArgs. This is what
// cmd/yastation-server's POST /commands/{name} and MCP tool handlers
// call — no slash-line is built or parsed anywhere on this path; see
// internal/dispatch.CallNamed. ok is false if name has no bound handler
// (either unregistered, or a REPL-only command with no named shape).
func (a *App) ExecuteNamed(ctx context.Context, name, station string, values map[string]string) (out string, ok bool, err error) {
	err = a.Queue.EnqueueWait(ctx, queue.Job{
		Label: "/" + name,
		Run: func() error {
			var runErr error
			out, ok, runErr = a.Dispatcher.CallNamed(ctx, name, station, values)
			return runErr
		},
	})
	return out, ok, err
}

// Close stops the queue, waiting for any in-flight job to finish.
func (a *App) Close() {
	a.Queue.Close()
}

func station(args []string) (string, []string) {
	// convention: an arg of the form station=Name picks a target speaker;
	// anywhere in the arg list, not just first/last.
	for i, a := range args {
		if strings.HasPrefix(a, "station=") {
			name := strings.TrimPrefix(a, "station=")
			rest := append(append([]string{}, args[:i]...), args[i+1:]...)
			return name, rest
		}
	}
	return "", args
}

// formatSoundList renders the sound_play catalog (the one /sound plays
// from) as a plain-text, category-grouped list — no markup, so it reads
// fine through a screen reader. filter (already lowercased) narrows it
// to ids/names containing that substring; empty filter lists everything.
func formatSoundList(filter string) string {
	byCategory := map[string][]sounds.Effect{}
	var order []string
	for _, e := range sounds.Effects() {
		if filter != "" &&
			!strings.Contains(strings.ToLower(e.ID), filter) &&
			!strings.Contains(strings.ToLower(e.NameRU), filter) &&
			!strings.Contains(strings.ToLower(e.Category), filter) {
			continue
		}
		if _, seen := byCategory[e.Category]; !seen {
			order = append(order, e.Category)
		}
		byCategory[e.Category] = append(byCategory[e.Category], e)
	}
	if len(order) == 0 {
		if filter == "" {
			return "звуков в каталоге нет"
		}
		return fmt.Sprintf("по запросу %q ничего не найдено", filter)
	}
	sort.Strings(order)
	var b strings.Builder
	total := 0
	for _, cat := range order {
		fmt.Fprintf(&b, "%s:\n", cat)
		list := byCategory[cat]
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
		for _, e := range list {
			fmt.Fprintf(&b, "  %s (%s)\n", e.ID, e.NameRU)
			total++
		}
	}
	fmt.Fprintf(&b, "Всего: %d", total)
	return b.String()
}

// formatSoundCategories lists only the category names in the sound_play
// catalog, with a per-category count — no individual sound ids/names.
func formatSoundCategories() string {
	counts := map[string]int{}
	for _, e := range sounds.Effects() {
		counts[e.Category]++
	}
	if len(counts) == 0 {
		return "звуков в каталоге нет"
	}
	cats := make([]string, 0, len(counts))
	for c := range counts {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	var b strings.Builder
	for _, c := range cats {
		fmt.Fprintf(&b, "%s (%d)\n", c, counts[c])
	}
	fmt.Fprintf(&b, "Всего категорий: %d", len(cats))
	return b.String()
}

// notifyVolume extracts an optional "volume=X" argument for /notify,
// defaulting to 0.4 (matching the reference behaviour) when absent.
// notifyVolume extracts an optional "volume=X" argument for /notify,
// defaulting to 4 (out of 0..10, matching the reference default of 40%)
// when absent.
func notifyVolume(args []string) (float64, []string) {
	for i, a := range args {
		if strings.HasPrefix(a, "volume=") {
			v, err := strconv.ParseFloat(strings.TrimPrefix(a, "volume="), 64)
			rest := append(append([]string{}, args[:i]...), args[i+1:]...)
			if err != nil {
				return 4, rest
			}
			return v, rest
		}
	}
	return 4, args
}

// truthy reports whether a string flag like "1"/"true"/"да" reads as on.
func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "да", "yes", "on":
		return true
	}
	return false
}

func (a *App) registerCommands() {
	d := a.Dispatcher

	// Free text with no "/" is TTS. A leading "- " is a voice-command
	// alias for /cmd, e.g. "- какая погода" instead of "/cmd какая погода".
	// A leading "~" is the whole-line whisper shortcut, mirroring "- ";
	// ";" works the same way and is the one to reach for on a Russian
	// keyboard layout — "~" needs a switch to Latin first (it's "ё"
	// otherwise), ";" doesn't.
	d.Default = func(ctx context.Context, text string) (string, error) {
		if rest, ok := strings.CutPrefix(text, "- "); ok {
			if err := a.Client.Command("", rest); err != nil {
				return "", err
			}
			return "Алиса услышала команду: " + rest, nil
		}
		whisperRest, isWhisper := strings.CutPrefix(text, "~")
		if !isWhisper {
			whisperRest, isWhisper = strings.CutPrefix(text, ";")
		}
		if isWhisper {
			rest := strings.TrimSpace(whisperRest)
			if rest == "" {
				return "", fmt.Errorf("после ~ (или ;) нужен текст")
			}
			expanded, err := expandSoundTags(rest)
			if err != nil {
				return "", err
			}
			if err := a.Client.SayWhisper("", expanded); err != nil {
				return "", err
			}
			return "[шёпотом] " + rest, nil
		}
		return speak(a.Client, "", text)
	}

	d.HandleBoundCat("Основное",
		"Сказать текст через станцию (TTS). ((текст)) — шёпотом отдельной репликой (слить с обычной речью в одну нельзя — целиком строку шёпотом: ~текст, ;текст или /whisper); [запрос] или №запрос№ — вставить встроенный звук Алисы прямо в речь (№ вместо [ ] удобнее на русской раскладке): /say привет ((это по секрету)) №гонг№",
		true,
		[]dispatch.Param{{Name: "text", Kind: "string", Help: "Текст для озвучивания"}},
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			text := v["text"]
			if text == "" {
				return "", fmt.Errorf("нужен текст: /say привет")
			}
			return speak(a.Client, station, text)
		}, "say", "s", "tts")

	d.HandleBoundCat("Основное", "Голосовая команда/вопрос Алисе. Ответ прозвучит из колонки, в консоль не возвращается",
		true,
		[]dispatch.Param{{Name: "text", Kind: "string", Help: "Текст голосовой команды/вопроса"}},
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			text := v["text"]
			if text == "" {
				return "", fmt.Errorf("нужен текст: /cmd включи радио")
			}
			if err := a.Client.Command(station, text); err != nil {
				return "", err
			}
			return "[команда отправлена] " + text, nil
		}, "cmd", "c", "ask")

	// batch runs several actions in ONE cloud scenario: stop, volume,
	// phrases (each split to fit quasar.MaxTTSChunkChars), then play.
	// Yandex executes them in order, Alice finishing each before the next —
	// the batch the local Glagol protocol can't do (see quasar.Client.Batch).
	d.HandleBoundCat("Основное",
		"Батч: несколько действий одним облачным сценарием — Алиса договаривает, делает следующее. /batch [stop] [volume] [play] [фразы через |] — например /batch 1 7 \"привет | как дела\". Внутри фраз работают ((шёпот)) и [звук]",
		true,
		[]dispatch.Param{
			{Name: "stop", Kind: "string", Optional: true, Help: "\"1\" — сначала остановить музыку"},
			{Name: "volume", Kind: "number", Optional: true, Help: "Громкость 0..10"},
			{Name: "play", Kind: "string", Optional: true, Help: "\"1\" — в конце продолжить музыку"},
			{Name: "phrases", Kind: "string", Optional: true, Help: "Текст для озвучивания; фразы через |, каждая режется до ~100 символов; ((шёпот)) и [звук] поддерживаются"},
		},
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			var actions []quasar.BatchAction
			if truthy(v["stop"]) {
				actions = append(actions, quasar.BatchAction{Kind: "cmd", Text: "останови"})
			}
			if vol := strings.TrimSpace(v["volume"]); vol != "" {
				if _, err := strconv.ParseFloat(vol, 64); err != nil {
					return "", fmt.Errorf("volume не число: %q", vol)
				}
				actions = append(actions, quasar.BatchAction{Kind: "cmd", Text: "громкость на " + vol})
			}
			says, err := batchActions(v["phrases"])
			if err != nil {
				return "", err
			}
			actions = append(actions, says...)
			if truthy(v["play"]) {
				actions = append(actions, quasar.BatchAction{Kind: "cmd", Text: "продолжить"})
			}
			if len(actions) == 0 {
				return "", fmt.Errorf("пустой батч — укажи хотя бы stop, volume, play или phrases")
			}
			if err := a.Client.Batch(station, actions); err != nil {
				return "", err
			}
			var parts []string
			for _, act := range actions {
				switch act.Kind {
				case "say":
					parts = append(parts, "сказать: "+act.Text)
				case "sound":
					parts = append(parts, "звук: "+act.SoundName)
				default:
					parts = append(parts, "команда: "+act.Text)
				}
			}
			return "[батч] " + strings.Join(parts, "; "), nil
		}, "batch")

	// read fetches a page and reads it aloud section by section: HTML is
	// reduced to its main content with headings kept, then batchActions
	// treats every heading as a new chunk (splitHeadings) so Alice reads the
	// article as digestible sections rather than one wall of text.
	// read keeps its own legacy REPL handler (HandleNamed, not
	// HandleBoundCat) because the documented slash syntax puts optional
	// [stop] [max] [play] flags before the URL — a plain positional bind
	// would swallow the URL into the first flag when called bare as
	// /read <url>. The bound handler (HTTP JSON bodies, the alice_read
	// MCP tool) is unaffected: there url is just its own named field.
	readBound := func(ctx context.Context, station string, v map[string]string) (string, error) {
			url := strings.TrimSpace(v["url"])
			if url == "" {
				return "", fmt.Errorf("нужен URL: /read https://...")
			}
			art, err := a.FetchArticle(ctx, url)
			if err != nil {
				return "", err
			}
			text, truncated := articleText(art, v["max"])
			actions, err := batchActions(text)
			if err != nil {
				return "", err
			}
			if len(actions) == 0 {
				return "", fmt.Errorf("на странице нет текста для чтения")
			}
			var acts []quasar.BatchAction
			if truthy(v["stop"]) {
				acts = append(acts, quasar.BatchAction{Kind: "cmd", Text: "останови"})
			}
			acts = append(acts, actions...)
			if truthy(v["play"]) {
				acts = append(acts, quasar.BatchAction{Kind: "cmd", Text: "продолжить"})
			}
			if err := a.Client.Batch(station, acts); err != nil {
				return "", err
			}
			title := cleanTitle(art.Title)
			if title == "" {
				title = url
			}
			out := fmt.Sprintf("[читать] %s — %d %s", title, len(actions), pluralRunes(len(actions), "шаг", "шага", "шагов"))
			if truncated {
				out += " (обрезано)"
			}
			return out, nil
		}
		readLegacy := func(ctx context.Context, args []string) (string, error) {
			station, rest := station(args)
			if len(rest) == 0 {
				return "", fmt.Errorf("нужен URL: /read https://...")
			}
			url := rest[len(rest)-1] // the URL is the last token — it can't contain spaces
			flags := rest[:len(rest)-1]
			values := map[string]string{"url": url}
			if len(flags) > 0 {
				values["stop"] = flags[0]
			}
			if len(flags) > 1 {
				values["max"] = flags[1]
			}
			if len(flags) > 2 {
				values["play"] = flags[2]
			}
			return readBound(ctx, station, values)
		}
		d.HandleNamed("Основное",
			"Прочитать статью или страницу по URL через Алису: стягивает страницу, вытаскивает текст (HTML → читаемые секции по заголовкам), зачитывает по частям. /read [stop] [max] [play] <url> — например /read 1 6000 1 https://habr.com/ru/articles/1/",
			true,
			[]dispatch.Param{
				{Name: "stop", Kind: "string", Optional: true, Help: "\"1\" — сначала остановить музыку"},
				{Name: "max", Kind: "number", Optional: true, Help: "Максимум рун текста для чтения (по умолчанию 6000)"},
				{Name: "play", Kind: "string", Optional: true, Help: "\"1\" — в конце продолжить музыку"},
				{Name: "url", Kind: "string", Help: "URL статьи или страницы"},
			},
			readLegacy, readBound, "read", "r", "article")

	// notify keeps its own legacy REPL handler (HandleNamed, not
	// HandleBoundCat) because its documented slash syntax puts volume=
	// as a keyword anywhere in the args rather than as the first
	// positional token (see README.md) — a plain positional bind would
	// silently change that syntax. The bound handler below (what HTTP
	// JSON bodies and the alice_notify MCP tool call) is unaffected:
	// there volume is just its own named field either way.
	notifyBound := func(ctx context.Context, station string, v map[string]string) (string, error) {
		volume := 4.0
		if raw := v["volume"]; raw != "" {
			parsed, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return "", fmt.Errorf("volume не число: %q", raw)
			}
			volume = parsed
		}
		text := v["text"]
		if text == "" {
			return "", fmt.Errorf("нужен текст: /notify задача выполнена")
		}
		if err := a.Client.Notify(station, text, volume); err != nil {
			return "", err
		}
		return fmt.Sprintf("[уведомление, громкость %v] %s", volume, text), nil
	}
	d.HandleNamed("Основное",
		"Уведомление: громкость (по умолчанию 4 из 0..10) + фраза. volume=3 в любом месте аргументов, volume=-1 пропустить громкость",
		true,
		[]dispatch.Param{
			{Name: "volume", Kind: "number", Optional: true, Help: "Громкость 0..10, по умолчанию 4; -1 — пропустить громкость"},
			{Name: "text", Kind: "string", Help: "Текст уведомления"},
		},
		func(ctx context.Context, args []string) (string, error) {
			station, rest := station(args)
			volume, rest := notifyVolume(rest)
			text := dispatch.Rest(rest)
			values := map[string]string{"text": text}
			if volume != 4 {
				values["volume"] = strconv.FormatFloat(volume, 'g', -1, 64)
			}
			return notifyBound(ctx, station, values)
		},
		notifyBound, "notify", "n")

	d.HandleBoundCat("Плеер", "Громкость 0..10, например /volume 3",
		true,
		[]dispatch.Param{{Name: "level", Kind: "number", Help: "Громкость, число от 0 до 10"}},
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			level, err := strconv.ParseFloat(v["level"], 64)
			if err != nil {
				return "", fmt.Errorf("не число: %q", v["level"])
			}
			if err := a.Client.Volume(station, level); err != nil {
				return "", err
			}
			return fmt.Sprintf("[громкость] %v", level), nil
		}, "volume", "vol")

	// play/pause/stop/next/prev/weather/news/timer/alarm/reminder used to
	// live here as near-identical little wrappers around Command(). They
	// now come from config.json (config.json.default), loaded and
	// registered by the caller (see RegisterCustomCommands) right after
	// New() returns — same mechanism as a user's own --config commands.

	d.HandleBoundCat("Сценарии", "Список твоих сценариев умного дома", false, nil,
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			names := a.Client.ListScenarios()
			if len(names) == 0 {
				return "сценариев не найдено", nil
			}
			return "- " + strings.Join(names, "\n- "), nil
		}, "scenarios")

	d.HandleBoundCat("Сценарии", "Запустить сценарий по имени: /scenario Вечер", false,
		[]dispatch.Param{{Name: "name", Kind: "string", Help: "Имя сценария"}},
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			name := v["name"]
			if name == "" {
				return "", fmt.Errorf("нужно имя сценария")
			}
			if err := a.Client.RunScenario(name); err != nil {
				return "", err
			}
			return "[сценарий запущен] " + name, nil
		}, "scenario", "run")

	d.HandleBoundCat("Диагностика", "Диагностика подключения", false, nil,
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			return a.Client.Diagnostics()
		}, "stations", "diag", "diagnostics")

	d.HandleBoundCat("Планировщик", "Периодическая команда: /every 5m /say время отчёта", false,
		[]dispatch.Param{
			{Name: "interval", Kind: "string", Help: "Период, например 30s, 5m, 2h"},
			{Name: "command_line", Kind: "string", Help: "Команда для запуска, в синтаксисе REPL, например \"/say время отчёта\""},
		},
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			commandLine := v["command_line"]
			if v["interval"] == "" || commandLine == "" {
				return "", fmt.Errorf("нужно: /every <30s|5m|2h> <команда>")
			}
			task, err := a.Scheduler.Schedule("every "+v["interval"], commandLine)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("[запланировано #%d] %s -> %s", task.ID, task.Spec, task.CommandLine), nil
		}, "every")

	d.HandleBoundCat("Планировщик", "Разовая команда в HH:MM: /at 7:30 /say доброе утро", false,
		[]dispatch.Param{
			{Name: "time", Kind: "string", Help: "Время в формате ЧЧ:ММ"},
			{Name: "command_line", Kind: "string", Help: "Команда для запуска, в синтаксисе REPL, например \"/say доброе утро\""},
		},
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			commandLine := v["command_line"]
			if v["time"] == "" || commandLine == "" {
				return "", fmt.Errorf("нужно: /at <ЧЧ:ММ> <команда>")
			}
			task, err := a.Scheduler.Schedule("at "+v["time"], commandLine)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("[запланировано #%d] %s -> %s", task.ID, task.Spec, task.CommandLine), nil
		}, "at")

	d.HandleBoundCat("Планировщик", "Список запланированных команд", false, nil,
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			tasks := a.Scheduler.List()
			if len(tasks) == 0 {
				return "активных задач нет", nil
			}
			var lines []string
			for _, t := range tasks {
				lines = append(lines, fmt.Sprintf("#%d %s -> %s", t.ID, t.Spec, t.CommandLine))
			}
			return strings.Join(lines, "\n"), nil
		}, "schedules", "jobs")

	d.HandleBoundCat("Планировщик", "Отменить все запланированные команды", false, nil,
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			a.Scheduler.CancelAll()
			return "[все запланированные команды отменены]", nil
		}, "unschedule_all", "cancel_all")

	d.HandleBoundCat("Планировщик", "Выполнить команды построчно из файла: /execute examples/morning.txt", false,
		[]dispatch.Param{{Name: "path", Kind: "string", Help: "Путь к файлу со списком команд"}},
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			if v["path"] == "" {
				return "", fmt.Errorf("нужен ровно один путь к файлу")
			}
			return a.executeScript(ctx, v["path"])
		}, "execute")

	d.HandleBoundCat("Экспериментально",
		"Скажи фразу шёпотом — через флаг whisper capability tts, подтверждённый на реальном устройстве, не фраза-угадайка. Можно вставить звук через [запрос]: /whisper текст",
		true,
		[]dispatch.Param{{Name: "text", Kind: "string", Help: "Текст для шёпота"}},
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			text := v["text"]
			if text == "" {
				return "", fmt.Errorf("нужен текст: /whisper тише едешь дальше будешь")
			}
			expanded, err := expandSoundTags(text)
			if err != nil {
				return "", err
			}
			if err := a.Client.SayWhisper(station, expanded); err != nil {
				return "", err
			}
			return "[шёпотом] " + text, nil
		}, "whisper", "шёпот")

	d.HandleBoundCat("Экспериментально",
		"Звук из библиотеки Алисы по (части) имени, RU или EN — если совпадение одно, id подставится сам: /sound бензопила, /sound explosion",
		true,
		[]dispatch.Param{{Name: "query", Kind: "string", Help: "Часть имени звука, RU или EN"}},
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			query := v["query"]
			if query == "" {
				return "", fmt.Errorf("нужно имя звука: /sound бензопила")
			}
			id, candidates, ok := sounds.FindEffect(query)
			if !ok {
				return "", fmt.Errorf("%s", sounds.FormatCandidates(query, candidates))
			}
			if err := a.Client.PlaySound(station, id, sounds.EffectNameByID(id)); err != nil {
				return "", err
			}
			return fmt.Sprintf("[звук] %s (запрос: %s)", id, query), nil
		}, "sound")

	d.HandleBoundCat("Экспериментально",
		"Список всех доступных звуков (для /sound), по категориям; можно сузить подстрокой RU/EN: /sounds, /sounds смех",
		false,
		[]dispatch.Param{{Name: "filter", Kind: "string", Optional: true, Help: "Подстрока для фильтра по id/имени/категории (RU/EN)"}},
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			filter := strings.ToLower(strings.TrimSpace(v["filter"]))
			return formatSoundList(filter), nil
		}, "sounds", "soundlist")

	d.HandleBoundCat("Экспериментально",
		"Только список категорий звуков (без раскрытия содержимого) — дальше сузить: /sounds <категория>",
		false, nil,
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			return formatSoundCategories(), nil
		}, "sndcat", "soundcategories")

	d.HandleBoundCat("Экспериментально",
		"Жёсткий стоп всего (не то же самое, что /stop): /stopall [станция]",
		true, nil,
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			if err := a.Client.StopEverything(station); err != nil {
				return "", err
			}
			return "[стоп всего]", nil
		}, "stopall")

	d.HandleBoundCat("Экспериментально",
		"Цветовая сцена подсветки колонки — без аргумента покажет список доступных сцен на этой станции (текстом, для неё не нужны глаза): /scene, /scene ночь",
		true,
		[]dispatch.Param{{Name: "name", Kind: "string", Optional: true, Help: "Часть имени/id сцены; без значения — список доступных сцен"}},
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			caps, err := a.Client.Capabilities(station)
			if err != nil {
				return "", err
			}
			options, hasScenes := quasar.SceneOptions(caps)
			if !hasScenes {
				return "", fmt.Errorf("на этой станции нет цветовых сцен (нет capability color_setting/scenes)")
			}
			if v["name"] == "" {
				if len(options) == 0 {
					return "цветовых сцен на этой станции нет", nil
				}
				var lines []string
				for _, o := range options {
					lines = append(lines, fmt.Sprintf("%s (%s)", o.ID, o.Name))
				}
				return "Доступные сцены:\n  " + strings.Join(lines, "\n  "), nil
			}

			query := strings.ToLower(v["name"])
			var id, matchedName string
			matches := 0
			for _, o := range options {
				if strings.EqualFold(o.ID, query) {
					id, matchedName = o.ID, o.Name
					matches = 1
					break
				}
				if strings.Contains(strings.ToLower(o.Name), query) || strings.Contains(strings.ToLower(o.ID), query) {
					id, matchedName = o.ID, o.Name
					matches++
				}
			}
			if matches == 0 {
				return "", fmt.Errorf("сцена не найдена: %q — наберите /scene без аргументов, чтобы увидеть список", query)
			}
			if matches > 1 {
				return "", fmt.Errorf("неоднозначный запрос %q, уточните — /scene без аргументов покажет список", query)
			}
			if err := a.Client.LightScene(station, id); err != nil {
				return "", err
			}
			return fmt.Sprintf("[сцена] %s (%s) — проверить можно через /refresh и /scene", id, matchedName), nil
		}, "scene", "light")

	d.HandleBoundCat("Диагностика",
		"Перечитать список станций/сценариев/capabilities с Яндекса — например, чтобы прочитать текстом, применилась ли смена сцены/звука, не дожидаясь визуальной проверки: /refresh",
		false, nil,
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			if err := a.Client.Refresh(); err != nil {
				return "", err
			}
			return "[обновлено]", nil
		}, "refresh")

	d.HandleBoundCat("Экспериментально",
		"Погода через структурную capability вместо фразы \"какая погода\" — подтверждено на реальном дампе, сработает ли запуск не проверено отдельно: /weather [станция]",
		true, nil,
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			if err := a.Client.Weather(station); err != nil {
				return "", err
			}
			return "[погода]", nil
		}, "weather")

	d.HandleBoundCat("Экспериментально",
		"Запустить музыку через структурную capability music_play (не то же самое, что /play — тот просто возобновляет паузу): /music [станция]",
		true, nil,
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			if err := a.Client.PlayMusic(station); err != nil {
				return "", err
			}
			return "[музыка]", nil
		}, "music")

	d.HandleBoundCat("Экспериментально",
		"Сырые capabilities станции как есть от Яндекса — для разведки протокола: /capabilities [станция]",
		true, nil,
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			caps, err := a.Client.Capabilities(station)
			if err != nil {
				return "", err
			}
			b, err := json.MarshalIndent(caps, "", "  ")
			if err != nil {
				return "", err
			}
			return string(b), nil
		}, "capabilities", "caps")

	d.HandleBoundCat("Экспериментально",
		"Сырой вызов capability в обход всех типизированных команд (см. /capabilities для имён): /raw тип instance значение",
		true,
		[]dispatch.Param{
			{Name: "cap_type", Kind: "string", Help: "Тип capability, например devices.capabilities.quasar.server_action"},
			{Name: "instance", Kind: "string", Help: "instance, например tts"},
			{Name: "value", Kind: "string", Help: "Значение — JSON или обычная строка, например {\"text\":\"привет\"}"},
		},
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			capType, instance, raw := v["cap_type"], v["instance"], v["value"]
			if capType == "" || instance == "" || raw == "" {
				return "", fmt.Errorf("нужно: /raw <тип> <instance> <значение>, например /raw devices.capabilities.quasar tts {\"text\":\"привет\"}")
			}
			var value any
			if err := json.Unmarshal([]byte(raw), &value); err != nil {
				value = raw // не JSON — шлём как обычную строку
			}
			if err := a.Client.RawCapability(station, capType, instance, value); err != nil {
				return "", err
			}
			return fmt.Sprintf("[raw %s/%s] отправлено: %v", capType, instance, value), nil
		}, "raw")

	d.HandleBoundCat("Справка", "Список команд (по /команда? — справка по одной)", false, nil,
		func(ctx context.Context, station string, v map[string]string) (string, error) {
			return d.Help(), nil
		}, "help", "h", "?")
}

// defaultMaxArticleRunes caps how much of a page /read will read aloud —
// enough for a long article, without risking a hundreds-of-steps scenario.
const defaultMaxArticleRunes = 6000

// articleText assembles the read-aloud text for an article: the title first
// (Alice announces what she's reading), then the body, capped at max runes
// (from the /read "max" param, else defaultMaxArticleRunes). Truncation cuts
// at the last heading boundary inside the window rather than mid-word, so the
// last section heard is still a complete one.
func articleText(art *article.Article, maxRaw string) (text string, truncated bool) {
	max := defaultMaxArticleRunes
	if maxRaw != "" {
		if n, err := strconv.Atoi(maxRaw); err == nil && n > 0 {
			max = n
		}
	}
	var parts []string
	if title := cleanTitle(art.Title); title != "" {
		parts = append(parts, "Заголовок: "+title)
	}
	if art.Text != "" {
		parts = append(parts, art.Text)
	}
	text = strings.Join(parts, "\n\n")
	runes := []rune(text)
	if len(runes) <= max {
		return text, false
	}
	cut := max
	if idx := strings.LastIndex(string(runes[:max]), "\n#"); idx > 0 {
		cut = idx
	}
	text = strings.TrimSpace(string(runes[:cut]))
	return text, true
}

// cleanTitle strips a trailing site-name segment off an HTML <title>
// ("Статья — Блог" / "Статья | Блог" / "Статья / Хабр") so Alice announces
// just the article title.
func cleanTitle(title string) string {
	for _, sep := range []string{" — ", " – ", " | ", " / "} {
		if i := strings.LastIndex(title, sep); i > 0 {
			title = strings.TrimSpace(title[:i])
		}
	}
	return title
}

// pluralRunes picks the Russian plural form for n: один шаг / два шага /
// пять шагов.
func pluralRunes(n int, one, few, many string) string {
	n %= 100
	if n >= 11 && n <= 14 {
		return many
	}
	switch n % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	}
	return many
}

// executeScript runs commands from a file line by line: blank lines and
// lines starting with # are skipped. Output from each line is collected
// and returned joined together (errors don't stop the script, they're
// reported inline, matching the old Python /execute behaviour).
func (a *App) executeScript(ctx context.Context, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("не смог прочитать %s: %w", path, err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		res, err := a.Dispatcher.Execute(ctx, trimmed)
		if err != nil {
			out = append(out, fmt.Sprintf("%s -> ошибка: %v", trimmed, err))
			continue
		}
		out = append(out, fmt.Sprintf("%s -> %s", trimmed, res))
	}
	return strings.Join(out, "\n"), nil
}
