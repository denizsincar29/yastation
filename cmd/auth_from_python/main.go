// Command auth_from_python converts tokens saved by the old Python
// prototype (yasg1988-style yandex_tokens.json: x_token/music_token/
// cookie-as-one-string/display_login) into this project's tokens.json
// format, so you don't have to redo the QR login if you still have an
// old checkout lying around.
//
// Usage:
//
//	go run ./cmd/auth_from_python /path/to/old/repo
//	go run ./cmd/auth_from_python /path/to/yandex_tokens.json
//
// The path can point directly at the JSON file, or at a directory (the
// old repo root, or anything above it) — in that case the tool searches
// for a file named yandex_tokens.json under it.
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/denizsincar29/yastation/internal/quasar"
)

type oldTokens struct {
	XToken       string `json:"x_token"`
	MusicToken   string `json:"music_token"`
	Cookie       string `json:"cookie"`
	DisplayLogin string `json:"display_login"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "использование: auth_from_python <путь к yandex_tokens.json или к старому репозиторию>")
		os.Exit(2)
	}

	jsonPath, err := locate(os.Args[1])
	if err != nil {
		fatal(err)
	}
	fmt.Println("Нашёл:", jsonPath)

	b, err := os.ReadFile(jsonPath)
	if err != nil {
		fatal(fmt.Errorf("не смог прочитать %s: %w", jsonPath, err))
	}
	var old oldTokens
	if err := json.Unmarshal(b, &old); err != nil {
		fatal(fmt.Errorf("не смог разобрать %s как старый формат токенов: %w", jsonPath, err))
	}
	if old.XToken == "" {
		fatal(fmt.Errorf("%s не похож на файл токенов (нет x_token)", jsonPath))
	}

	cookies := quasar.CookiesFromHeaderString(old.Cookie, ".yandex.ru", "/")
	if err := quasar.SaveRaw(old.XToken, cookies, ""); err != nil {
		fatal(err)
	}

	fmt.Println("Готово. Сконвертировано и сохранено в", quasar.TokenFilePath())
	if old.DisplayLogin != "" {
		fmt.Println("Аккаунт:", old.DisplayLogin)
	}
	fmt.Println("Куки могли протухнуть с момента последнего запуска старой версии — если")
	fmt.Println("подключение не заработает сразу, проще всего перелогиниться: go run ./cmd/yastation-auth")
}

// locate resolves p to an actual yandex_tokens.json file: p itself if
// it's already a file, otherwise the first yandex_tokens.json found
// walking the directory tree under p.
func locate(p string) (string, error) {
	info, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("не смог найти %s: %w", p, err)
	}
	if !info.IsDir() {
		return p, nil
	}

	var found string
	err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries instead of aborting the whole walk
		}
		if !d.IsDir() && d.Name() == "yandex_tokens.json" {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("не нашёл yandex_tokens.json внутри %s", p)
	}
	return found, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "Ошибка:", err)
	os.Exit(1)
}
