// Command yastation is the interactive REPL: connects to the station,
// then reads commands from stdin. Free text (no leading /) is spoken by
// the default speaker; everything else follows the /help table.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"

	"github.com/denizsincar29/yastation/internal/app"
	"github.com/denizsincar29/yastation/internal/quasar"
)

func main() {
	fmt.Println("Подключаюсь к Яндекс Станции...")
	client, err := quasar.Connect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Не удалось подключиться:", err)
		fmt.Fprintln(os.Stderr, "Если это первый запуск — авторизуйтесь: go run ./cmd/yastation-auth")
		os.Exit(1)
	}
	fmt.Printf("Подключено. Колонок найдено: %d\n", len(client.Speakers))
	fmt.Println("Пиши текст — он будет озвучен станцией. Команды — с /, /help — список.")

	a := app.New(client)
	defer a.Close()

	ctx := context.Background()
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
