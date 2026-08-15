// Command yastation-soundcheck walks every id in internal/sounds'
// sound_play catalog, plays each one on a real station via
// Client.PlaySound, and logs which ids the Яндекс API actually accepted
// (HTTP 200 / scenario ran) vs rejected (BAD_REQUEST and friends).
//
// The sound_play catalog was hand-assembled and only one entry
// ("chainsaw-1") was ever confirmed against a real capabilities dump —
// see internal/sounds/sound_play.json and PROTOCOL_NOTES.md. This tool
// re-verifies the whole list against reality so the JSON can be trimmed
// down to only ids that actually work.
//
// Usage:
//
//	go run ./cmd/yastation-soundcheck                    # full run, default speaker
//	go run ./cmd/yastation-soundcheck -station "Кухня"    # target a specific speaker
//	go run ./cmd/yastation-soundcheck -delay 4s           # pause between sounds (let them finish)
//	go run ./cmd/yastation-soundcheck -resume             # skip ids already in -out from a previous run
//	go run ./cmd/yastation-soundcheck -filter explosion   # only check ids/names containing this substring
//	go run ./cmd/yastation-soundcheck -ids-file candidates_stripped.json   # test id hypotheses not yet in the catalog
//	                                                       # (see cmd/yastation-soundcheck/candidates_stripped.json:
//	                                                       # for every failed original id like "human-cough-1", every
//	                                                       # progressive hyphen-strip is a candidate — "cough-1",
//	                                                       # and for 3+ segment ids every intermediate strip too)
//
// Results are written incrementally to -out (default sound_check_results.json)
// as {"id":..., "name_ru":..., "ok":bool, "error":"..."} lines (JSON Lines,
// one object per line) so an interrupted run doesn't lose progress and
// -resume can pick up where it left off.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/denizsincar29/yastation/internal/quasar"
	"github.com/denizsincar29/yastation/internal/sounds"
)

// result is one line of the JSONL output/resume file.
type result struct {
	ID      string `json:"id"`
	NameRU  string `json:"name_ru"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Checked string `json:"checked_at"`
}

func main() {
	var station string
	var delay time.Duration
	var outPath string
	var resume bool
	var filter string
	var idsFile string
	flag.StringVar(&station, "station", "", "имя/id колонки (по умолчанию — колонка по умолчанию)")
	flag.DurationVar(&delay, "delay", 3*time.Second, "пауза между звуками, чтобы каждый успел доиграть")
	flag.StringVar(&outPath, "out", "sound_check_results.jsonl", "куда писать результаты (JSON Lines)")
	flag.BoolVar(&resume, "resume", false, "пропустить id, уже отмеченные в -out от прошлого запуска")
	flag.StringVar(&filter, "filter", "", "проверять только id/имена, содержащие эту подстроку")
	flag.StringVar(&idsFile, "ids-file", "", "проверить id из этого JSON-файла ([{\"id\":..,\"name_ru\":..}, ...]) вместо каталога sound_play.json — для проверки id-гипотез, которых ещё нет в каталоге")
	flag.Parse()

	already := map[string]bool{}
	if resume {
		already = loadDone(outPath)
	}

	effects := sounds.Effects()
	if idsFile != "" {
		custom, err := loadCandidates(idsFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "не могу прочитать", idsFile, ":", err)
			os.Exit(1)
		}
		effects = custom
	}
	var todo []sounds.Effect
	for _, e := range effects {
		if filter != "" && !strings.Contains(strings.ToLower(e.ID), strings.ToLower(filter)) &&
			!strings.Contains(strings.ToLower(e.NameRU), strings.ToLower(filter)) {
			continue
		}
		if already[e.ID] {
			continue
		}
		todo = append(todo, e)
	}
	if len(todo) == 0 {
		fmt.Println("Нечего проверять (всё отфильтровано или уже пройдено с -resume).")
		return
	}

	fmt.Println("Подключаюсь к Яндекс Станции...")
	client, err := quasar.Connect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Не удалось подключиться:", err)
		fmt.Fprintln(os.Stderr, "Если это первый запуск — авторизуйтесь: go run ./cmd/yastation-auth")
		os.Exit(1)
	}
	fmt.Printf("Подключено. Колонок: %d. Проверяю %d звук(ов) из %d, пауза %s между ними.\n",
		len(client.Speakers), len(todo), len(effects), delay)

	f, err := os.OpenFile(outPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Не могу открыть", outPath, ":", err)
		os.Exit(1)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	var okCount, failCount int
	for i, e := range todo {
		err := client.PlaySound(station, e.ID, e.NameRU)
		r := result{
			ID:      e.ID,
			NameRU:  e.NameRU,
			OK:      err == nil,
			Checked: time.Now().Format(time.RFC3339),
		}
		if err != nil {
			r.Error = err.Error()
			failCount++
			fmt.Printf("[%d/%d] %-28s (%s) -> ОШИБКА: %v\n", i+1, len(todo), e.ID, e.NameRU, err)
		} else {
			okCount++
			fmt.Printf("[%d/%d] %-28s (%s) -> ok\n", i+1, len(todo), e.ID, e.NameRU)
		}

		line, _ := json.Marshal(r)
		w.Write(line)
		w.WriteByte('\n')
		w.Flush() // flush every line so Ctrl+C / crash doesn't lose progress

		if i < len(todo)-1 {
			time.Sleep(delay)
		}
	}

	fmt.Printf("\nГотово: %d ок, %d ошибок. Результаты дописаны в %s\n", okCount, failCount, outPath)
	if failCount > 0 {
		fmt.Println("Дальше: go run ./cmd/yastation-soundcheck-apply -in", outPath, "чтобы пересобрать sound_play.json только из ok:true")
	}
}

// loadCandidates reads an ad-hoc list of id hypotheses to check, in the
// same shape as sound_play.json, for testing ids that aren't (yet) in
// the catalog at all.
func loadCandidates(path string) ([]sounds.Effect, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var effects []sounds.Effect
	if err := json.Unmarshal(data, &effects); err != nil {
		return nil, err
	}
	return effects, nil
}

// loadDone reads a previous JSONL run and returns the set of ids it
// already has an entry for, regardless of ok/fail — a failed id doesn't
// need re-checking either unless the person explicitly deletes it from
// the file first.
func loadDone(path string) map[string]bool {
	done := map[string]bool{}
	f, err := os.Open(path)
	if err != nil {
		return done // no previous run, nothing to resume
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r result
		if err := json.Unmarshal(sc.Bytes(), &r); err == nil && r.ID != "" {
			done[r.ID] = true
		}
	}
	return done
}
