// Package quasar talks to the undocumented Yandex Quasar/Passport API used
// by the mobile Yandex app to control Alice speakers. This is an
// independent reimplementation written from understanding the public
// Quasar HTTP endpoints and the QR-login flow used by Yandex's own web
// clients — no code was copied from any third-party project. Yandex may
// change this API at any time without notice.
package quasar

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var csrfRe = regexp.MustCompile(`__CSRF__ = "([^"]+)`)
var csrf2Re = regexp.MustCompile(`"csrfToken2":"(.+?)"`)

// Session wraps an authenticated HTTP client for Yandex's passport and
// quasar (smart home) APIs, mirroring the auth dance the official mobile
// app performs (QR login -> session cookie -> OAuth x-token).
type Session struct {
	HTTP   *http.Client
	Jar    *cookiejar.Jar
	Domain string // override yandex.ru with e.g. yandex.com if needed

	XToken     string
	MusicToken string

	csrfToken   string
	authHeaders map[string]string
	authJSON    map[string]any

	lastRequest time.Time
}

// NewSession creates an empty session with its own cookie jar.
func NewSession() (*Session, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("cookiejar: %w", err)
	}
	return &Session{
		HTTP: &http.Client{Jar: jar, Timeout: 20 * time.Second},
		Jar:  jar,
	}, nil
}

func (s *Session) rewriteURL(rawURL string) string {
	if s.Domain == "" {
		return rawURL
	}
	return strings.ReplaceAll(rawURL, "yandex.ru", s.Domain)
}

type reqOpts struct {
	headers map[string]string
	form    url.Values
	json    any
}

func (s *Session) rawDo(method, rawURL string, opts reqOpts) (*http.Response, error) {
	var body io.Reader
	contentType := ""
	switch {
	case opts.json != nil:
		b, err := json.Marshal(opts.json)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
		contentType = "application/json"
	case opts.form != nil:
		body = strings.NewReader(opts.form.Encode())
		contentType = "application/x-www-form-urlencoded"
	}

	req, err := http.NewRequest(method, s.rewriteURL(rawURL), body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range opts.headers {
		req.Header.Set(k, v)
	}
	return s.HTTP.Do(req)
}

// --- QR login -----------------------------------------------------------

// GetQR starts the QR-login flow and returns a link the user should open
// and confirm on their phone/browser.
func (s *Session) GetQR() (string, error) {
	resp, err := s.rawDo(http.MethodGet, "https://passport.yandex.ru/pwl-yandex", reqOpts{})
	if err != nil {
		return "", err
	}
	html, err := readAndClose(resp)
	if err != nil {
		return "", err
	}
	m := csrfRe.FindStringSubmatch(html)
	if m == nil {
		return "", fmt.Errorf("не удалось получить CSRF для QR-авторизации")
	}
	s.authHeaders = map[string]string{"X-CSRF-Token": m[1]}

	resp, err = s.rawDo(http.MethodPost,
		"https://passport.yandex.ru/pwl-yandex/api/passport/auth/password/submit",
		reqOpts{headers: s.authHeaders, json: map[string]string{"retpath": "https://passport.yandex.ru/"}},
	)
	if err != nil {
		return "", err
	}
	if err := decodeJSONClose(resp, &s.authJSON); err != nil {
		return "", err
	}

	trackID, _ := s.authJSON["track_id"].(string)
	resp, err = s.rawDo(http.MethodPost,
		"https://passport.yandex.ru/pwl-yandex/api/passport/auth/magic/code",
		reqOpts{headers: s.authHeaders, form: url.Values{
			"location_id":    {"0"},
			"magic_track_id": {trackID},
			"track_id":       {""},
		}},
	)
	if err != nil {
		return "", err
	}
	var data map[string]any
	if err := decodeJSONClose(resp, &data); err != nil {
		return "", err
	}
	link, _ := data["link"].(string)
	if link == "" {
		return "", fmt.Errorf("яндекс не вернул ссылку для QR: %v", data)
	}
	return link, nil
}

// LoginQR polls once whether the QR code has been confirmed. It returns
// (nil, nil) if the user hasn't confirmed yet — call again after a short
// delay. Call GetQR first.
func (s *Session) LoginQR() (map[string]any, error) {
	if s.authJSON == nil || s.authHeaders == nil {
		return nil, fmt.Errorf("QR-авторизация не начата")
	}
	resp, err := s.rawDo(http.MethodPost,
		"https://passport.yandex.ru/pwl-yandex/api/passport/auth/magic/code/status",
		reqOpts{headers: s.authHeaders, json: s.authJSON},
	)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := decodeJSONClose(resp, &data); err != nil {
		return nil, err
	}
	if data["state"] != "otp_auth_finished" {
		return nil, nil
	}

	trackID, _ := data["trackId"].(string)
	resp, err = s.rawDo(http.MethodPost,
		"https://passport.yandex.ru/pwl-yandex/api/passport/sessions/get_session",
		reqOpts{headers: s.authHeaders, form: url.Values{"track_id": {trackID}}},
	)
	if err != nil {
		return nil, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	return s.LoginCookies()
}

// LoginCookies exchanges the current browser-like session cookies for an
// OAuth x-token (the same token the mobile app uses for API calls).
func (s *Session) LoginCookies() (map[string]any, error) {
	u, _ := url.Parse("https://passport.yandex.ru")
	var cookiePairs []string
	for _, c := range s.Jar.Cookies(u) {
		cookiePairs = append(cookiePairs, c.Name+"="+c.Value)
	}
	cookieHeader := strings.Join(cookiePairs, "; ")

	resp, err := s.rawDo(http.MethodPost,
		"https://mobileproxy.passport.yandex.net/1/bundle/oauth/token_by_sessionid",
		reqOpts{form: url.Values{
			"client_id":     {"c0ebe342af7d48fbbbfcf2d2eedb8f9e"},
			"client_secret": {"ad0a908f0aa341a182a37ecd75bc319e"},
		}, headers: map[string]string{
			"Ya-Client-Host":   "passport.yandex.ru",
			"Ya-Client-Cookie": cookieHeader,
		}},
	)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := decodeJSONClose(resp, &data); err != nil {
		return nil, err
	}
	xToken, _ := data["access_token"].(string)
	if xToken == "" {
		return nil, fmt.Errorf("яндекс не вернул OAuth token: %v", data)
	}
	return s.ValidateToken(xToken)
}

// ValidateToken checks an x-token is alive and stores it on the session.
func (s *Session) ValidateToken(xToken string) (map[string]any, error) {
	resp, err := s.rawDo(http.MethodGet,
		"https://mobileproxy.passport.yandex.net/1/bundle/account/short_info/?avatar_size=islands-300",
		reqOpts{headers: map[string]string{"Authorization": "OAuth " + xToken}},
	)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := decodeJSONClose(resp, &data); err != nil {
		return nil, err
	}
	if data["status"] != "ok" {
		return nil, fmt.Errorf("токен яндекса не прошёл проверку: %v", data)
	}
	data["x_token"] = xToken
	s.XToken = xToken
	return data, nil
}

// LoginToken re-establishes browser cookies from a saved x-token (used on
// every startup instead of doing a fresh QR login).
func (s *Session) LoginToken(xToken string) (bool, error) {
	resp, err := s.rawDo(http.MethodPost,
		"https://mobileproxy.passport.yandex.net/1/bundle/auth/x_token/",
		reqOpts{form: url.Values{"type": {"x-token"}, "retpath": {"https://www.yandex.ru"}},
			headers: map[string]string{"Ya-Consumer-Authorization": "OAuth " + xToken}},
	)
	if err != nil {
		return false, err
	}
	var data map[string]any
	if err := decodeJSONClose(resp, &data); err != nil {
		return false, err
	}
	if data["status"] != "ok" {
		return false, nil
	}
	passportHost, _ := data["passport_host"].(string)
	trackID, _ := data["track_id"].(string)

	client := &http.Client{
		Jar:     s.Jar,
		Timeout: 20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequest(http.MethodGet, passportHost+"/auth/session/?track_id="+url.QueryEscape(trackID), nil)
	if err != nil {
		return false, err
	}
	resp2, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp2.Body.Close()
	io.Copy(io.Discard, resp2.Body)
	loc := resp2.Header.Get("Location")
	return strings.Contains(loc, "/auth/finish"), nil
}

// RefreshCookies makes sure the session has a valid quasar cookie,
// re-logging in via the stored x-token if needed.
func (s *Session) RefreshCookies() (bool, error) {
	resp, err := s.rawDo(http.MethodGet, "https://yandex.ru/quasar?storage=1", reqOpts{})
	if err != nil {
		return false, err
	}
	var data map[string]any
	if err := decodeJSONClose(resp, &data); err == nil {
		if storage, ok := data["storage"].(map[string]any); ok {
			if user, ok := storage["user"].(map[string]any); ok {
				if uid, _ := user["uid"].(string); uid != "" {
					return true, nil
				}
			}
		}
	}
	if s.XToken == "" {
		return false, nil
	}
	return s.LoginToken(s.XToken)
}

// --- Generic authenticated request with CSRF + 401/403 retry ------------

// Request performs an authenticated call to a quasar/iot endpoint,
// fetching a CSRF token first for non-GET methods and retrying once on
// 401 (refresh cookies) or 403 (refresh CSRF token), matching the retry
// behaviour of the official web client.
func (s *Session) Request(method, rawURL string, body any) (*http.Response, error) {
	return s.request(method, rawURL, body, 2)
}

func (s *Session) request(method, rawURL string, body any, retriesLeft int) (*http.Response, error) {
	// simple rate limit: ~5 req/s, like the reference clients do
	if wait := 200*time.Millisecond - time.Since(s.lastRequest); wait > 0 {
		time.Sleep(wait)
	}
	s.lastRequest = time.Now()

	headers := map[string]string{}
	if method != http.MethodGet {
		if s.csrfToken == "" {
			if err := s.fetchCSRF(); err != nil {
				return nil, err
			}
		}
		headers["x-csrf-token"] = s.csrfToken
	}

	resp, err := s.rawDo(method, rawURL, reqOpts{headers: headers, json: body})
	if err != nil {
		return nil, err
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return resp, nil
	case http.StatusUnauthorized:
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if retriesLeft <= 0 {
			return nil, fmt.Errorf("%s вернул HTTP 401", rawURL)
		}
		if _, err := s.RefreshCookies(); err != nil {
			return nil, err
		}
		return s.request(method, rawURL, body, retriesLeft-1)
	case http.StatusForbidden:
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if retriesLeft <= 0 {
			return nil, fmt.Errorf("%s вернул HTTP 403", rawURL)
		}
		s.csrfToken = ""
		return s.request(method, rawURL, body, retriesLeft-1)
	default:
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if len(b) > 500 {
			b = b[:500]
		}
		return nil, fmt.Errorf("%s вернул HTTP %d: %s", rawURL, resp.StatusCode, string(b))
	}
}

func (s *Session) fetchCSRF() error {
	resp, err := s.rawDo(http.MethodGet, "https://yandex.ru/quasar", reqOpts{})
	if err != nil {
		return err
	}
	html, err := readAndClose(resp)
	if err != nil {
		return err
	}
	m := csrf2Re.FindStringSubmatch(html)
	if m == nil {
		return fmt.Errorf("не удалось получить CSRF токен яндекса")
	}
	s.csrfToken = m[1]
	return nil
}

// --- helpers --------------------------------------------------------------

func readAndClose(resp *http.Response) (string, error) {
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodeJSONClose(resp *http.Response, out any) error {
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	return dec.Decode(out)
}
