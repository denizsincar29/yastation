package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/denizsincar29/yastation/internal/quasar"
)

// fakeAuthSession builds a *quasar.Session usable as loginViaQR's return
// value in tests: a real Session (so sess.WhoAmI() inside
// pendingAuthStore.succeed doesn't nil-panic), but with a short HTTP
// timeout so if it does try to reach the real login.yandex.ru (which
// this sandbox can't route to anyway), it fails fast instead of hanging
// — WhoAmI's result is best-effort and its error is intentionally
// ignored by succeed().
func fakeAuthSession(xToken string) *quasar.Session {
	sess, err := quasar.NewSession()
	if err != nil {
		panic(err) // NewSession only fails if cookiejar.New does, which never happens with a nil options arg
	}
	sess.XToken = xToken
	sess.HTTP.Timeout = 200 * time.Millisecond
	return sess
}

func TestPendingAuthStoreLifecycle(t *testing.T) {
	store := newPendingAuthStore(time.Minute)

	store.create("id1")
	pa, ok := store.get("id1")
	if !ok || pa.status != authPending {
		t.Fatalf("expected a pending entry right after create, got %+v (ok=%v)", pa, ok)
	}

	store.succeed("id1", fakeAuthSession("tok-123"))
	pa, ok = store.get("id1")
	if !ok || pa.status != authDone || pa.xToken != "tok-123" {
		t.Fatalf("expected done with xToken=tok-123, got %+v (ok=%v)", pa, ok)
	}

	store.create("id2")
	store.fail("id2", fmt.Errorf("boom"))
	pa, ok = store.get("id2")
	if !ok || pa.status != authFailed || pa.err == nil {
		t.Fatalf("expected failed with an error, got %+v (ok=%v)", pa, ok)
	}
}

func TestPendingAuthStoreSweepsExpiredEntries(t *testing.T) {
	store := newPendingAuthStore(30 * time.Millisecond)
	store.create("id1") // plain create — no network call, so no timing slack needed

	time.Sleep(80 * time.Millisecond)
	if _, ok := store.get("id1"); ok {
		t.Fatalf("expected id1 to be swept away after ttl")
	}
}

func TestHandleAuthStartRedirectsToYandexLink(t *testing.T) {
	const wantLink = "https://yandex.ru/some/confirm/link"
	orig := loginViaQR
	defer func() { loginViaQR = orig }()
	loginViaQR = func(timeout time.Duration, onLink func(string)) (*quasar.Session, error) {
		onLink(wantLink)
		return fakeAuthSession("does-not-matter-here"), nil
	}

	store := newPendingAuthStore(time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/auth/start", nil)
	rec := httptest.NewRecorder()
	handleAuthStart(store)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, wantLink) {
		t.Fatalf("expected the Yandex link in the page, got: %s", body)
	}
	if !strings.Contains(body, "/auth/result?id=") {
		t.Fatalf("expected a link/refresh to /auth/result?id=..., got: %s", body)
	}
}

func TestHandleAuthStartSurfacesImmediateFailure(t *testing.T) {
	orig := loginViaQR
	defer func() { loginViaQR = orig }()
	loginViaQR = func(timeout time.Duration, onLink func(string)) (*quasar.Session, error) {
		return nil, fmt.Errorf("яндекс недоступен")
	}

	store := newPendingAuthStore(time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/auth/start", nil)
	rec := httptest.NewRecorder()
	handleAuthStart(store)(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "яндекс недоступен") {
		t.Fatalf("expected the underlying error in the page, got: %s", rec.Body.String())
	}
}

func TestHandleAuthResultPending(t *testing.T) {
	store := newPendingAuthStore(time.Minute)
	store.create("abc")

	req := httptest.NewRequest(http.MethodGet, "/auth/result?id=abc", nil)
	rec := httptest.NewRecorder()
	handleAuthResult(store)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `http-equiv="refresh"`) {
		t.Fatalf("expected an auto-refresh meta tag while pending, got: %s", body)
	}
	if strings.Contains(body, "x-token") {
		t.Fatalf("must not leak anything token-shaped while still pending: %s", body)
	}
}

func TestHandleAuthResultDoneShowsToken(t *testing.T) {
	store := newPendingAuthStore(time.Minute)
	store.create("abc")
	sess := fakeAuthSession("super-secret-token")
	store.succeed("abc", sess)

	req := httptest.NewRequest(http.MethodGet, "/auth/result?id=abc", nil)
	rec := httptest.NewRecorder()
	handleAuthResult(store)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "super-secret-token") {
		t.Fatalf("expected the token in the done page, got: %s", rec.Body.String())
	}
}

func TestHandleAuthResultUnknownID(t *testing.T) {
	store := newPendingAuthStore(time.Minute)

	req := httptest.NewRequest(http.MethodGet, "/auth/result?id=doesnotexist", nil)
	rec := httptest.NewRecorder()
	handleAuthResult(store)(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown id, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "/auth/start") {
		t.Fatalf("expected a link back to /auth/start, got: %s", rec.Body.String())
	}
}
