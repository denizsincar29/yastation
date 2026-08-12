// Command yastation-access manages access.json, the allowlist
// yastation-server checks an incoming X-Yandex-Token's account against
// in bring-your-own-token mode (see internal/access).
//
// "add" runs the same QR-login flow as yastation-auth, but the person
// scanning the QR code doesn't have to be you — send them the printed
// link (or have them scan it themselves) and once they confirm, this
// tool resolves their account's name via quasar.WhoAmI and adds *that
// identity* to the allowlist. Their actual OAuth token is never written
// to disk here; only their name/uid is kept. They keep bringing their
// own live X-Yandex-Token on every request afterwards, same as before —
// this only decides whether that token's owner is allowed at all.
//
// Nothing here needs a browser or an open port on the machine it runs
// on: the QR link is meant to be opened on a different device (the
// account owner's phone), so this works the same over SSH as it does on
// a desktop.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/denizsincar29/yastation/internal/access"
	"github.com/denizsincar29/yastation/internal/quasar"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	path := access.FilePath()
	switch os.Args[1] {
	case "list", "ls":
		cmdList(path)
	case "add":
		name := strings.Join(os.Args[2:], " ")
		cmdAdd(path, name)
	case "remove", "rm":
		if len(os.Args) < 3 {
			fatal(fmt.Errorf("нужно: yastation-access remove <uid|логин|часть имени>"))
		}
		cmdRemove(path, strings.Join(os.Args[2:], " "))
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "неизвестная команда: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "использование:")
	fmt.Fprintln(os.Stderr, "  yastation-access list                    список допущенных аккаунтов")
	fmt.Fprintln(os.Stderr, "  yastation-access add [имя]                добавить аккаунт (QR-вход)")
	fmt.Fprintln(os.Stderr, "  yastation-access remove <uid|логин|имя>   отозвать доступ")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Файл списка:", access.FilePath())
	fmt.Fprintln(os.Stderr, "(путь можно переопределить: YASTATION_ACCESS_FILE)")
}

func cmdList(path string) {
	l, err := access.Load(path)
	if err != nil {
		fatal(err)
	}
	if len(l.Entries) == 0 {
		fmt.Println("список пуст:", path)
		return
	}
	fmt.Printf("%s (%d):\n", path, len(l.Entries))
	for _, e := range l.Entries {
		login := e.Login
		if login == "" {
			login = "-"
		}
		fmt.Printf("  %-24s login=%-16s uid=%-14s добавлен %s\n",
			e.Name, login, e.UID, e.AddedAt.Format("2006-01-02 15:04"))
	}
}

func cmdAdd(path, presetName string) {
	fmt.Println("Открой ссылку и подтверди вход в Яндекс — можно на СВОЁМ телефоне, если")
	fmt.Println("добавляешь чужой аккаунт: перешли ссылку тому человеку, пусть подтвердит сам.")
	fmt.Println("Токен нигде не сохраняется — только имя и uid, которые вернёт Яндекс.")
	fmt.Println()

	sess, err := quasar.LoginViaQR(3*time.Minute, func(link string) {
		fmt.Println("Ссылка:", link)
	})
	if err != nil {
		fatal(err)
	}

	id, err := sess.WhoAmI()
	if err != nil {
		fatal(fmt.Errorf("вошли, но не смог узнать, кто это: %w", err))
	}

	name := presetName
	if name == "" {
		name = id.RealName
		if name == "" {
			name = id.DisplayName
		}
		if name == "" {
			name = id.Login
		}
	}
	fmt.Printf("\nЯндекс говорит: %s (login=%s, uid=%s)\n", firstNonEmpty(id.RealName, id.DisplayName, "?"), id.Login, id.UID)
	name = askWithDefault(fmt.Sprintf("Имя для списка [%s]", name), name)

	l, err := access.Load(path)
	if err != nil {
		fatal(err)
	}
	_, existed := l.Find(id.UID)
	l.Add(access.Entry{Name: name, UID: id.UID, Login: id.Login})
	if err := access.Save(path, l); err != nil {
		fatal(err)
	}

	if existed {
		fmt.Printf("Обновлено: %s (uid=%s) — %s\n", name, id.UID, path)
	} else {
		fmt.Printf("Добавлено: %s (uid=%s) — %s\n", name, id.UID, path)
	}
}

func cmdRemove(path, query string) {
	l, err := access.Load(path)
	if err != nil {
		fatal(err)
	}
	e, ok := l.FindByQuery(query)
	if !ok {
		fmt.Fprintf(os.Stderr, "не нашёл однозначно %q в списке. Сейчас в списке:\n", query)
		for _, e := range l.Entries {
			fmt.Fprintf(os.Stderr, "  %s (login=%s, uid=%s)\n", e.Name, e.Login, e.UID)
		}
		os.Exit(1)
	}
	if !confirm(fmt.Sprintf("Отозвать доступ у %s (uid=%s)?", e.Name, e.UID)) {
		fmt.Println("Отменено.")
		return
	}
	l.Remove(e.UID)
	if err := access.Save(path, l); err != nil {
		fatal(err)
	}
	fmt.Printf("Удалено: %s (uid=%s) — %s\n", e.Name, e.UID, path)
}

func askWithDefault(prompt, def string) string {
	fmt.Printf("%s: ", prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func confirm(prompt string) bool {
	fmt.Printf("%s [y/N]: ", prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes" || line == "д" || line == "да"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "Ошибка:", err)
	os.Exit(1)
}
