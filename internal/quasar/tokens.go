package quasar

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// StoredTokens is what gets written to disk after a successful login, and
// read back on every subsequent start so we don't have to QR-login again.
type StoredTokens struct {
	XToken  string       `json:"x_token"`
	Cookies []cookieJSON `json:"cookies"`
	Domain  string       `json:"domain,omitempty"`
}

type cookieJSON struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
	Path   string `json:"path"`
}

// TokenFilePath resolves where tokens live: $YASTATION_TOKEN_FILE if set,
// otherwise <user config dir>/yastation/tokens.json.
func TokenFilePath() string {
	if p := os.Getenv("YASTATION_TOKEN_FILE"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "yastation", "tokens.json")
}

// SaveTokens writes the session's x-token and cookie jar to disk.
func SaveTokens(sess *Session) error {
	u, _ := url.Parse("https://passport.yandex.ru")
	var cookies []cookieJSON
	for _, c := range sess.Jar.Cookies(u) {
		cookies = append(cookies, cookieJSON{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path})
	}
	return SaveRaw(sess.XToken, cookies, sess.Domain)
}

// SaveRaw writes a token set to TokenFilePath() directly, without needing
// a live Session. Used both by SaveTokens (after a real QR login) and by
// cmd/auth_from_python (converting tokens saved by the old Python
// prototype, which stored cookies as a single "name=value; ..." string
// rather than structured objects).
func SaveRaw(xToken string, cookies []cookieJSON, domain string) error {
	path := TokenFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data := StoredTokens{XToken: xToken, Cookies: cookies, Domain: domain}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// CookiesFromHeaderString parses a "name1=value1; name2=value2" cookie
// header string (the format the old Python prototype stored under the
// "cookie" key) into the structured form this package expects, assuming
// they all belong to the given domain/path — which is a safe assumption
// here since that's exactly how the Python client collected them (see
// cmd/auth_from_python).
func CookiesFromHeaderString(header, domain, path string) []cookieJSON {
	var out []cookieJSON
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		out = append(out, cookieJSON{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value), Domain: domain, Path: path})
	}
	return out
}

// LoadTokens reads tokens.json and re-populates a fresh Session's cookie
// jar and x-token. Returns an error if the file doesn't exist yet (first
// run — caller should fall back to QR login).
func LoadTokens() (*Session, error) {
	path := TokenFilePath()
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("файл токенов не найден (%s), нужна авторизация: %w", path, err)
	}
	var data StoredTokens
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, fmt.Errorf("файл токенов повреждён: %w", err)
	}
	sess, err := NewSession()
	if err != nil {
		return nil, err
	}
	sess.XToken = data.XToken
	sess.Domain = data.Domain
	u, _ := url.Parse("https://passport.yandex.ru")
	var httpCookies []*http.Cookie
	for _, c := range data.Cookies {
		httpCookies = append(httpCookies, &http.Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path})
	}
	sess.Jar.SetCookies(u, httpCookies)
	return sess, nil
}
