package quasar

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// loginInfoURL is a var, not a const, so tests can point it at a local
// httptest server instead of the real Yandex API.
var loginInfoURL = "https://login.yandex.ru/info?format=json"

// Identity is the Yandex account behind an OAuth token — resolved via
// https://login.yandex.ru/info, the same "who am I" call Yandex's own
// apps use to show your name in the corner. UID is Yandex's stable,
// globally unique account id — the actual key to check against an
// allowlist. Login/DisplayName/RealName are for humans reading
// access.json, never for access decisions (display names collide,
// people rename themselves; UID doesn't).
type Identity struct {
	UID         string `json:"uid"`
	Login       string `json:"login"`
	DisplayName string `json:"display_name"`
	RealName    string `json:"real_name"`
}

// WhoAmI resolves the account identity behind this session's OAuth
// token (Session.XToken). Used by yastation-server to check an incoming
// X-Yandex-Token against internal/access's allowlist, and by
// cmd/yastation-access to find out who just scanned a QR code before
// adding them.
//
// This calls Yandex's general-purpose Login API, not anything Quasar/
// smart-home specific — it should work with any valid Passport OAuth
// token regardless of which app/scopes it was issued under, but that's
// based on how login.yandex.ru/info behaves for the token type this
// project's QR flow produces; if a particular token comes back without
// an id, the error message says so explicitly rather than silently
// treating it as "not allowed".
func (s *Session) WhoAmI() (Identity, error) {
	if s.XToken == "" {
		return Identity{}, fmt.Errorf("нет OAuth-токена в сессии")
	}
	resp, err := s.rawDo(http.MethodGet, loginInfoURL, reqOpts{
		headers: map[string]string{"Authorization": "OAuth " + s.XToken},
	})
	if err != nil {
		return Identity{}, fmt.Errorf("login.yandex.ru/info: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Identity{}, fmt.Errorf("login.yandex.ru/info: чтение ответа: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Identity{}, fmt.Errorf("login.yandex.ru/info вернул HTTP %d: %s", resp.StatusCode, string(body))
	}
	return parseWhoAmI(body)
}

func parseWhoAmI(body []byte) (Identity, error) {
	var wire struct {
		ID          string `json:"id"`
		Login       string `json:"login"`
		DisplayName string `json:"display_name"`
		RealName    string `json:"real_name"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return Identity{}, fmt.Errorf("не смог разобрать ответ login.yandex.ru: %w", err)
	}
	if wire.ID == "" {
		return Identity{}, fmt.Errorf("яндекс не вернул id аккаунта — токен без прав login:info или недействителен")
	}
	return Identity{UID: wire.ID, Login: wire.Login, DisplayName: wire.DisplayName, RealName: wire.RealName}, nil
}

// WhoAmIFromXToken is the standalone version of WhoAmI for callers that
// only have a raw x-token, not a whole logged-in Session (e.g.
// yastation-server, checking an incoming X-Yandex-Token before spending
// any effort building a full Client for it).
func WhoAmIFromXToken(xToken string) (Identity, error) {
	sess, err := NewSession()
	if err != nil {
		return Identity{}, err
	}
	sess.XToken = xToken
	return sess.WhoAmI()
}
