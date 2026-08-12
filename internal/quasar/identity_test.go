package quasar

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseWhoAmIHappyPath(t *testing.T) {
	body := []byte(`{"id":"123456789","login":"deniz.s","display_name":"Deniz","real_name":"Deniz Sincar"}`)
	id, err := parseWhoAmI(body)
	if err != nil {
		t.Fatal(err)
	}
	if id.UID != "123456789" || id.Login != "deniz.s" || id.RealName != "Deniz Sincar" {
		t.Fatalf("id=%+v", id)
	}
}

func TestParseWhoAmIMissingIDErrors(t *testing.T) {
	_, err := parseWhoAmI([]byte(`{"login":"deniz.s"}`))
	if err == nil {
		t.Fatal("expected error for response with no id")
	}
}

func TestParseWhoAmIInvalidJSONErrors(t *testing.T) {
	_, err := parseWhoAmI([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestWhoAmIRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "OAuth test-token" {
			t.Errorf("Authorization header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"42","login":"deniz","display_name":"Deniz","real_name":"Deniz Sincar"}`))
	}))
	defer srv.Close()

	orig := loginInfoURL
	loginInfoURL = srv.URL
	defer func() { loginInfoURL = orig }()

	sess, err := NewSession()
	if err != nil {
		t.Fatal(err)
	}
	sess.XToken = "test-token"

	id, err := sess.WhoAmI()
	if err != nil {
		t.Fatal(err)
	}
	if id.UID != "42" || id.RealName != "Deniz Sincar" {
		t.Fatalf("id=%+v", id)
	}
}

func TestWhoAmIWithoutTokenErrors(t *testing.T) {
	sess, err := NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.WhoAmI(); err == nil {
		t.Fatal("expected error for session without an XToken")
	}
}

func TestWhoAmISurfacesHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`invalid_token`))
	}))
	defer srv.Close()

	orig := loginInfoURL
	loginInfoURL = srv.URL
	defer func() { loginInfoURL = orig }()

	sess, err := NewSession()
	if err != nil {
		t.Fatal(err)
	}
	sess.XToken = "bad-token"
	if _, err := sess.WhoAmI(); err == nil {
		t.Fatal("expected error for HTTP 401")
	}
}
