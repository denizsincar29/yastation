// A browser-friendly way to get your own x-token for X-Yandex-Token,
// without touching a terminal: /auth/start kicks off the exact same
// quasar.LoginViaQR flow cmd/yastation-auth uses, but redirects your
// browser to the confirm link instead of printing a QR code; /auth/result
// polls it and shows the resulting token to copy into a client config.
//
// This is *not* OAuth — there's no redirect_uri, no code exchange, no
// token endpoint, nothing an MCP client could drive automatically. It's
// a convenience page for a human to fetch a token by hand, meant to be
// bookmarked/opened manually. Purely additive: doesn't touch this
// server's own tokens.json/default account, doesn't write anything to
// disk, doesn't add anyone to access.json — it just shows you a string.
package main

import (
	"fmt"
	"html"
	"net/http"
	"sync"
	"time"

	"crypto/rand"
	"encoding/hex"

	"github.com/denizsincar29/yastation/internal/quasar"
)

// loginViaQR is quasar.LoginViaQR behind a variable so tests can swap it
// for a fake — same pattern as tokenClientCache.resolveIdentity/buildClient.
var loginViaQR = quasar.LoginViaQR

type authStatus int

const (
	authPending authStatus = iota
	authDone
	authFailed
)

type pendingAuth struct {
	status    authStatus
	xToken    string
	identity  quasar.Identity
	err       error
	createdAt time.Time
}

// pendingAuthStore holds in-flight/just-finished browser auth attempts,
// keyed by a random id handed to the browser via the /auth/result?id=
// query string. Entries are dropped after ttl regardless of outcome —
// there's no "mark as viewed and delete" step, so an accidental page
// refresh right after seeing the token doesn't break anything; the TTL
// alone bounds how long a token sits in server memory.
type pendingAuthStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]*pendingAuth
}

func newPendingAuthStore(ttl time.Duration) *pendingAuthStore {
	return &pendingAuthStore{ttl: ttl, entries: map[string]*pendingAuth{}}
}

func (s *pendingAuthStore) sweepLocked() {
	now := time.Now()
	for k, e := range s.entries {
		if now.Sub(e.createdAt) > s.ttl {
			delete(s.entries, k)
		}
	}
}

func (s *pendingAuthStore) create(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	s.entries[id] = &pendingAuth{status: authPending, createdAt: time.Now()}
}

func (s *pendingAuthStore) get(id string) (pendingAuth, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	e, ok := s.entries[id]
	if !ok {
		return pendingAuth{}, false
	}
	return *e, true // copy out — caller doesn't get a pointer into the map
}

func (s *pendingAuthStore) fail(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[id]; ok {
		e.status = authFailed
		e.err = err
	}
}

func (s *pendingAuthStore) succeed(id string, sess *quasar.Session) {
	identity, _ := sess.WhoAmI() // best-effort — a name/uid on the result page is a nice-to-have, not required
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[id]; ok {
		e.status = authDone
		e.xToken = sess.XToken
		e.identity = identity
	}
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// handleAuthStart begins a login attempt: opens a quasar session, waits
// for the Yandex confirm link, and redirects the browser to a page with
// that link plus a way to check back for the result. The actual
// polling-for-confirmation continues in the background after this
// handler returns (see loginViaQR's own timeout) — /auth/result picks up
// the outcome whenever the browser checks back.
func handleAuthStart(store *pendingAuthStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := randomID()
		if err != nil {
			renderAuthError(w, http.StatusInternalServerError, fmt.Errorf("не смог сгенерировать id сессии: %w", err))
			return
		}
		store.create(id)

		type linkOrErr struct {
			link string
			err  error
		}
		linkCh := make(chan linkOrErr, 1)
		var sendOnce sync.Once

		go func() {
			sess, err := loginViaQR(3*time.Minute, func(link string) {
				sendOnce.Do(func() { linkCh <- linkOrErr{link: link} })
			})
			if err != nil {
				// Covers two cases with one path: the session/QR couldn't
				// even be started (onLink never ran, so the select below
				// is still waiting on this) and the poll timing out after
				// the link was already shown (send is a no-op by then,
				// sendOnce already fired) — either way /auth/result needs
				// to see the failure.
				sendOnce.Do(func() { linkCh <- linkOrErr{err: err} })
				store.fail(id, err)
				return
			}
			store.succeed(id, sess)
		}()

		select {
		case res := <-linkCh:
			if res.err != nil {
				store.fail(id, res.err)
				renderAuthError(w, http.StatusBadGateway, fmt.Errorf("не смог начать вход через Яндекс: %w", res.err))
				return
			}
			renderAuthStartPage(w, id, res.link)
		case <-time.After(20 * time.Second):
			store.fail(id, fmt.Errorf("Яндекс не ответил вовремя"))
			renderAuthError(w, http.StatusGatewayTimeout, fmt.Errorf("Яндекс не ответил вовремя, попробуй ещё раз"))
		}
	}
}

// handleAuthResult reports where a login attempt from /auth/start
// stands: still waiting (auto-refreshing page), failed, or done (shows
// the x-token to copy).
func handleAuthResult(store *pendingAuthStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		pa, ok := store.get(id)
		if !ok {
			renderAuthError(w, http.StatusNotFound, fmt.Errorf("ссылка устарела или не найдена — начни заново"))
			return
		}
		switch pa.status {
		case authFailed:
			renderAuthError(w, http.StatusBadGateway, pa.err)
		case authDone:
			renderAuthDonePage(w, pa)
		default:
			renderAuthWaitingPage(w, id)
		}
	}
}

// --- plain, screen-reader-friendly HTML — no JS, no CSS framework,
// real headings, a manual link alongside every auto-refresh so nothing
// depends on meta-refresh actually firing. ---

const htmlHead = `<!DOCTYPE html>
<html lang="ru">
<head><meta charset="utf-8"><title>%s — yastation</title>%s</head>
<body>
`

func renderAuthStartPage(w http.ResponseWriter, id, link string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	resultURL := "/auth/result?id=" + id
	fmt.Fprintf(w, htmlHead, "Вход в Яндекс", `<meta http-equiv="refresh" content="4;url=`+html.EscapeString(resultURL)+`">`)
	fmt.Fprintf(w, `<h1>Вход в Яндекс</h1>
<p>Открой ссылку ниже (в новой вкладке — эта страница останется открытой) и подтверди вход в свой Яндекс-аккаунт:</p>
<p><a href="%s" target="_blank" rel="noopener">Войти через Яндекс</a></p>
<p>Эта страница сама перейдёт дальше через несколько секунд. Если нет — открой сюда: <a href="%s">получить токен</a>.</p>
</body></html>`, html.EscapeString(link), html.EscapeString(resultURL))
}

func renderAuthWaitingPage(w http.ResponseWriter, id string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, htmlHead, "Ждём подтверждения", `<meta http-equiv="refresh" content="4">`)
	fmt.Fprintf(w, `<h1>Ждём подтверждения…</h1>
<p>Если уже подтвердил вход в Яндексе — подожди немного, страница обновится сама.</p>
<p>Если ссылку ещё не открывал(а) или прошло больше пары минут — <a href="/auth/start">начни заново</a>.</p>
</body></html>`)
}

func renderAuthDonePage(w http.ResponseWriter, pa pendingAuth) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	who := pa.identity.RealName
	if who == "" {
		who = pa.identity.Login
	}
	fmt.Fprintf(w, htmlHead, "Готово", "")
	fmt.Fprintf(w, `<h1>Готово</h1>
`)
	if who != "" {
		fmt.Fprintf(w, `<p>Вход выполнен: %s (uid %s).</p>
`, html.EscapeString(who), html.EscapeString(pa.identity.UID))
	}
	fmt.Fprintf(w, `<p>Вставь это значение в заголовок <code>X-Yandex-Token</code> своего MCP/HTTP-клиента:</p>
<p><textarea readonly rows="3" cols="70" wrap="off" aria-label="x-token, для копирования">%s</textarea></p>
<p><strong>Никому не показывай этот токен — с ним можно полностью управлять твоей Яндекс Станцией от твоего имени.</strong></p>
<p>Эта страница перестанет показывать токен примерно через 10 минут после входа — если понадобится снова, повтори через <a href="/auth/start">/auth/start</a>.</p>
</body></html>`, html.EscapeString(pa.xToken))
}

func renderAuthError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, htmlHead, "Ошибка", "")
	fmt.Fprintf(w, `<h1>Ошибка</h1>
<p>%s</p>
<p><a href="/auth/start">Попробовать снова</a></p>
</body></html>`, html.EscapeString(err.Error()))
}
