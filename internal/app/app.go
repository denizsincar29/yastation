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
	"strconv"
	"strings"

	"github.com/denizsincar29/yastation/internal/dispatch"
	"github.com/denizsincar29/yastation/internal/queue"
	"github.com/denizsincar29/yastation/internal/scheduler"
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
	PlaySound(station, soundID string) error
	StopEverything(station string) error
	LightScene(station, sceneID string) error
	Weather(station string) error
	PlayMusic(station string) error
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
}

// New builds an App around an already-connected station client
// (typically a *quasar.Client, or a fake in tests).
func New(client StationAPI) *App {
	a := &App{Client: client}
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

func (a *App) registerCommands() {
	d := a.Dispatcher

	// Free text with no "/" is TTS. A leading "- " is a voice-command
	// alias for /cmd, e.g. "- какая погода" instead of "/cmd какая погода".
	d.Default = func(ctx context.Context, text string) (string, error) {
		if rest, ok := strings.CutPrefix(text, "- "); ok {
			if err := a.Client.Command("", rest); err != nil {
				return "", err
			}
			return "Алиса услышала команду: " + rest, nil
		}
		if err := a.Client.Say("", text); err != nil {
			return "", err
		}
		return "Алиса сказала: " + text, nil
	}

	d.HandleCat("Основное", "Сказать текст через станцию (TTS)", func(ctx context.Context, args []string) (string, error) {
		station, rest := station(args)
		text := dispatch.Rest(rest)
		if text == "" {
			return "", fmt.Errorf("нужен текст: /say привет")
		}
		if err := a.Client.Say(station, text); err != nil {
			return "", err
		}
		return "Алиса сказала: " + text, nil
	}, "say", "s", "tts")

	d.HandleCat("Основное", "Голосовая команда/вопрос Алисе. Ответ прозвучит из колонки, в консоль не возвращается", func(ctx context.Context, args []string) (string, error) {
		station, rest := station(args)
		text := dispatch.Rest(rest)
		if text == "" {
			return "", fmt.Errorf("нужен текст: /cmd включи радио")
		}
		if err := a.Client.Command(station, text); err != nil {
			return "", err
		}
		return "[команда отправлена] " + text, nil
	}, "cmd", "c", "ask")

	d.HandleCat("Основное",
		"Уведомление: громкость (по умолчанию 0.4) + фраза. volume=0.3 в любом месте аргументов, volume=-1 пропустить громкость",
		func(ctx context.Context, args []string) (string, error) {
			station, rest := station(args)
			volume, rest := notifyVolume(rest)
			text := dispatch.Rest(rest)
			if text == "" {
				return "", fmt.Errorf("нужен текст: /notify задача выполнена")
			}
			if err := a.Client.Notify(station, text, volume); err != nil {
				return "", err
			}
			return fmt.Sprintf("[уведомление, громкость %v] %s", volume, text), nil
		}, "notify", "n")

	d.HandleCat("Плеер", "Громкость 0..10, например /volume 3", func(ctx context.Context, args []string) (string, error) {
		station, rest := station(args)
		if len(rest) != 1 {
			return "", fmt.Errorf("нужно ровно одно число: /volume 3")
		}
		level, err := strconv.ParseFloat(rest[0], 64)
		if err != nil {
			return "", fmt.Errorf("не число: %q", rest[0])
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

	d.HandleCat("Сценарии", "Список твоих сценариев умного дома", func(ctx context.Context, args []string) (string, error) {
		names := a.Client.ListScenarios()
		if len(names) == 0 {
			return "сценариев не найдено", nil
		}
		return "- " + strings.Join(names, "\n- "), nil
	}, "scenarios")

	d.HandleCat("Сценарии", "Запустить сценарий по имени: /scenario Вечер", func(ctx context.Context, args []string) (string, error) {
		name := dispatch.Rest(args)
		if name == "" {
			return "", fmt.Errorf("нужно имя сценария")
		}
		if err := a.Client.RunScenario(name); err != nil {
			return "", err
		}
		return "[сценарий запущен] " + name, nil
	}, "scenario", "run")

	d.HandleCat("Диагностика", "Диагностика подключения", func(ctx context.Context, args []string) (string, error) {
		return a.Client.Diagnostics()
	}, "stations", "diag", "diagnostics")

	d.HandleCat("Планировщик", "Периодическая команда: /every 5m /say время отчёта", func(ctx context.Context, args []string) (string, error) {
		if len(args) < 2 {
			return "", fmt.Errorf("нужно: /every <30s|5m|2h> <команда>")
		}
		interval := args[0]
		commandLine := dispatch.Rest(args[1:])
		task, err := a.Scheduler.Schedule("every "+interval, commandLine)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("[запланировано #%d] %s -> %s", task.ID, task.Spec, task.CommandLine), nil
	}, "every")

	d.HandleCat("Планировщик", "Разовая команда в HH:MM: /at 7:30 /say доброе утро", func(ctx context.Context, args []string) (string, error) {
		if len(args) < 2 {
			return "", fmt.Errorf("нужно: /at <ЧЧ:ММ> <команда>")
		}
		at := args[0]
		commandLine := dispatch.Rest(args[1:])
		task, err := a.Scheduler.Schedule("at "+at, commandLine)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("[запланировано #%d] %s -> %s", task.ID, task.Spec, task.CommandLine), nil
	}, "at")

	d.HandleCat("Планировщик", "Список запланированных команд", func(ctx context.Context, args []string) (string, error) {
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

	d.HandleCat("Планировщик", "Отменить все запланированные команды", func(ctx context.Context, args []string) (string, error) {
		a.Scheduler.CancelAll()
		return "[все запланированные команды отменены]", nil
	}, "unschedule_all", "cancel_all")

	d.HandleCat("Планировщик", "Выполнить команды построчно из файла: /execute examples/morning.txt", func(ctx context.Context, args []string) (string, error) {
		if len(args) != 1 {
			return "", fmt.Errorf("нужен ровно один путь к файлу")
		}
		return a.executeScript(ctx, args[0])
	}, "execute")

	d.HandleCat("Экспериментально",
		"Скажи фразу шёпотом — через флаг whisper capability tts, подтверждённый на реальном устройстве, не фраза-угадайка: /whisper текст",
		func(ctx context.Context, args []string) (string, error) {
			station, rest := station(args)
			text := dispatch.Rest(rest)
			if text == "" {
				return "", fmt.Errorf("нужен текст: /whisper тише едешь дальше будешь")
			}
			if err := a.Client.SayWhisper(station, text); err != nil {
				return "", err
			}
			return "[шёпотом] " + text, nil
		}, "whisper", "шёпот")

	d.HandleCat("Экспериментально",
		"Звук из библиотеки Алисы по id (см. /capabilities -> sound_play после того, как попросишь звук голосом): /sound chainsaw-1",
		func(ctx context.Context, args []string) (string, error) {
			station, rest := station(args)
			if len(rest) == 0 {
				return "", fmt.Errorf("нужен id звука: /sound chainsaw-1")
			}
			id := rest[0]
			if err := a.Client.PlaySound(station, id); err != nil {
				return "", err
			}
			return "[звук] " + id, nil
		}, "sound")

	d.HandleCat("Экспериментально",
		"Жёсткий стоп всего (не то же самое, что /stop): /stopall [станция]",
		func(ctx context.Context, args []string) (string, error) {
			st, _ := station(args)
			if err := a.Client.StopEverything(st); err != nil {
				return "", err
			}
			return "[стоп всего]", nil
		}, "stopall")

	d.HandleCat("Экспериментально",
		"Цветовая сцена подсветки колонки по id (см. /capabilities -> color_setting/scenes для списка на твоём устройстве): /scene night",
		func(ctx context.Context, args []string) (string, error) {
			station, rest := station(args)
			if len(rest) == 0 {
				return "", fmt.Errorf("нужен id сцены: /scene night")
			}
			id := rest[0]
			if err := a.Client.LightScene(station, id); err != nil {
				return "", err
			}
			return "[сцена] " + id, nil
		}, "scene", "light")

	d.HandleCat("Экспериментально",
		"Погода через структурную capability вместо фразы \"какая погода\" — подтверждено на реальном дампе, сработает ли запуск не проверено отдельно: /weather [станция]",
		func(ctx context.Context, args []string) (string, error) {
			st, _ := station(args)
			if err := a.Client.Weather(st); err != nil {
				return "", err
			}
			return "[погода]", nil
		}, "weather")

	d.HandleCat("Экспериментально",
		"Запустить музыку через структурную capability music_play (не то же самое, что /play — тот просто возобновляет паузу): /music [станция]",
		func(ctx context.Context, args []string) (string, error) {
			st, _ := station(args)
			if err := a.Client.PlayMusic(st); err != nil {
				return "", err
			}
			return "[музыка]", nil
		}, "music")

	d.HandleCat("Экспериментально",
		"Сырые capabilities станции как есть от Яндекса — для разведки протокола: /capabilities [станция]",
		func(ctx context.Context, args []string) (string, error) {
			st, _ := station(args)
			caps, err := a.Client.Capabilities(st)
			if err != nil {
				return "", err
			}
			b, err := json.MarshalIndent(caps, "", "  ")
			if err != nil {
				return "", err
			}
			return string(b), nil
		}, "capabilities", "caps")

	d.HandleCat("Экспериментально",
		"Сырой вызов capability в обход всех типизированных команд (см. /capabilities для имён): /raw тип instance значение",
		func(ctx context.Context, args []string) (string, error) {
			st, rest := station(args)
			if len(rest) < 3 {
				return "", fmt.Errorf("нужно: /raw <тип> <instance> <значение>, например /raw devices.capabilities.quasar tts {\"text\":\"привет\"}")
			}
			capType, instance := rest[0], rest[1]
			raw := dispatch.Rest(rest[2:])
			var value any
			if err := json.Unmarshal([]byte(raw), &value); err != nil {
				value = raw // не JSON — шлём как обычную строку
			}
			if err := a.Client.RawCapability(st, capType, instance, value); err != nil {
				return "", err
			}
			return fmt.Sprintf("[raw %s/%s] отправлено: %v", capType, instance, value), nil
		}, "raw")

	d.HandleCat("Справка", "Список команд (по /команда? — справка по одной)", func(ctx context.Context, args []string) (string, error) {
		return d.Help(), nil
	}, "help", "h", "?")
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
