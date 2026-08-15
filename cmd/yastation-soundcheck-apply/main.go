// Command yastation-soundcheck-apply reads the JSONL results produced
// by yastation-soundcheck and rebuilds internal/sounds/sound_play.json
// keeping only ids that were confirmed to actually work (ok:true).
//
// Ids that came back BAD_REQUEST (ok:false) are dropped and printed as
// "removed". Ids from the current catalog that were never checked at all
// (not present in the results file — e.g. you used -filter or stopped
// early) are kept as-is and printed as "unverified, kept", so a partial
// run never silently deletes untested entries.
//
// Usage:
//
//	go run ./cmd/yastation-soundcheck-apply -in sound_check_results.jsonl            # dry run, just prints the plan
//	go run ./cmd/yastation-soundcheck-apply -in sound_check_results.jsonl -write     # actually rewrite sound_play.json
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/denizsincar29/yastation/internal/sounds"
)

type result struct {
	ID     string `json:"id"`
	NameRU string `json:"name_ru"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

func main() {
	var inPath, outPath string
	var write bool
	flag.StringVar(&inPath, "in", "sound_check_results.jsonl", "JSONL с результатами yastation-soundcheck")
	flag.StringVar(&outPath, "sound-play-json", "internal/sounds/sound_play.json", "путь к каталогу, который нужно пересобрать")
	flag.BoolVar(&write, "write", false, "реально перезаписать файл (без флага — только показать план)")
	flag.Parse()

	f, err := os.Open(inPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "не могу открыть", inPath, ":", err)
		os.Exit(1)
	}
	defer f.Close()

	checked := map[string]bool{} // id -> ok
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r result
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil || r.ID == "" {
			continue
		}
		checked[r.ID] = r.OK // last line for an id wins, so a re-check after edit overrides
	}

	var kept []sounds.Effect
	var removed, unverified []string
	for _, e := range sounds.Effects() {
		ok, was := checked[e.ID]
		switch {
		case was && ok:
			kept = append(kept, e)
		case was && !ok:
			removed = append(removed, fmt.Sprintf("%s (%s)", e.ID, e.NameRU))
		default:
			unverified = append(unverified, fmt.Sprintf("%s (%s)", e.ID, e.NameRU))
			kept = append(kept, e) // never checked -> keep, don't guess
		}
	}

	sort.Slice(kept, func(i, j int) bool { return kept[i].ID < kept[j].ID })

	fmt.Printf("Подтверждено и остаётся: %d\n", len(kept)-len(unverified))
	fmt.Printf("Удаляется (BAD_REQUEST на реальном устройстве): %d\n", len(removed))
	for _, s := range removed {
		fmt.Println("  -", s)
	}
	fmt.Printf("Не проверялись в этом прогоне, оставлены как есть: %d\n", len(unverified))
	for _, s := range unverified {
		fmt.Println("  ?", s)
	}

	if !write {
		fmt.Println("\nЭто был dry-run. Добавь -write, чтобы реально переписать", outPath)
		return
	}

	out, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка сериализации:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(outPath, append(out, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "не могу записать", outPath, ":", err)
		os.Exit(1)
	}
	fmt.Println("\nЗаписано в", outPath)
}
