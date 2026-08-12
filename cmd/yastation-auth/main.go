// Command yastation-auth performs the QR login flow once and saves the
// resulting tokens so yastation/yastation-server don't need to log in
// again on every start.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/denizsincar29/yastation/internal/quasar"
)

func main() {
	sess, err := quasar.LoginViaQR(3*time.Minute, func(link string) {
		fmt.Println("Откройте ссылку и подтвердите вход в Яндекс:")
		fmt.Println(link)
		openBrowser(link)
	})
	if err != nil {
		fatal(err)
	}
	if err := quasar.SaveTokens(sess); err != nil {
		fatal(err)
	}
	fmt.Println("Готово. Токены сохранены в", quasar.TokenFilePath())
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start() // best effort; the printed link is the real fallback
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "Ошибка:", err)
	os.Exit(1)
}
