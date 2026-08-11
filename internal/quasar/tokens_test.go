package quasar

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCookiesFromHeaderString(t *testing.T) {
	got := CookiesFromHeaderString("Session_id=abc123; yandexuid=xyz; ", ".yandex.ru", "/")
	if len(got) != 2 {
		t.Fatalf("expected 2 cookies, got %d: %v", len(got), got)
	}
	if got[0].Name != "Session_id" || got[0].Value != "abc123" || got[0].Domain != ".yandex.ru" || got[0].Path != "/" {
		t.Fatalf("cookie[0] = %+v", got[0])
	}
	if got[1].Name != "yandexuid" || got[1].Value != "xyz" {
		t.Fatalf("cookie[1] = %+v", got[1])
	}
}

func TestCookiesFromHeaderStringSkipsGarbage(t *testing.T) {
	got := CookiesFromHeaderString("noequalsign; ; a=b", ".yandex.ru", "/")
	if len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("got %v", got)
	}
}

func TestSaveRawThenLoadTokensRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	t.Setenv("YASTATION_TOKEN_FILE", path)

	cookies := CookiesFromHeaderString("a=1; b=2", ".yandex.ru", "/")
	if err := SaveRaw("my-x-token", cookies, ""); err != nil {
		t.Fatalf("SaveRaw: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file at %s: %v", path, err)
	}

	sess, err := LoadTokens()
	if err != nil {
		t.Fatalf("LoadTokens: %v", err)
	}
	if sess.XToken != "my-x-token" {
		t.Fatalf("XToken = %q", sess.XToken)
	}
}
