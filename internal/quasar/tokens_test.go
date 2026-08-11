package quasar

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveRawThenLoadTokensRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	t.Setenv("YASTATION_TOKEN_FILE", path)

	cookies := []cookieJSON{{Name: "a", Value: "1", Domain: ".yandex.ru", Path: "/"}}
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
