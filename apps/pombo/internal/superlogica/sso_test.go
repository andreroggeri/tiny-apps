package superlogica

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSite stands in for the condo site and Keycloak. They must answer on
// different hostnames: net/http's cookie jar keys on host, so 127.0.0.1 and
// localhost are used to keep the two sets of cookies apart.
type fakeSite struct {
	condoURL, ssoURL string
	ssoHost          string
	ssoLoggedIn      bool
	formServed       int
}

func newFakeSite(t *testing.T, ssoLoggedIn bool) *fakeSite {
	t.Helper()
	f := &fakeSite{ssoLoggedIn: ssoLoggedIn}

	sso := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("KEYCLOAK_IDENTITY"); err != nil || !f.ssoLoggedIn {
			f.formServed++
			w.Write([]byte(`<html><form id="kc-form-login">...</form></html>`))
			return
		}
		// Keycloak rotates its cookie on a silent re-auth.
		http.SetCookie(w, &http.Cookie{Name: "KEYCLOAK_IDENTITY", Value: "rotated", Path: "/"})
		http.Redirect(w, r, f.condoURL+"/clients/areadocondomino/publico/oidccallback?code=abc", http.StatusFound)
	}))
	f.ssoURL = strings.Replace(sso.URL, "127.0.0.1", "localhost", 1)
	u, err := url.Parse(f.ssoURL)
	if err != nil {
		t.Fatal(err)
	}
	f.ssoHost = u.Host

	condo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/clients/areadocondomino":
			http.Redirect(w, r, f.ssoURL+"/auth/realms/superlogica-cloud/protocol/openid-connect/auth", http.StatusFound)
		case "/clients/areadocondomino/publico/oidccallback":
			http.SetCookie(w, &http.Cookie{Name: "PHPSESSID", Value: "fresh", Path: "/"})
			http.Redirect(w, r, "/clients/areadocondomino/inicio", http.StatusFound)
		case "/clients/areadocondomino/inicio":
			w.Write([]byte("<html>Bem-vindo</html>"))
		case "/clients/areadocondomino/reservas":
			if _, err := r.Cookie("PHPSESSID"); err != nil {
				http.Redirect(w, r, f.ssoURL+"/auth/realms/superlogica-cloud/protocol/openid-connect/auth", http.StatusFound)
				return
			}
			w.Write([]byte("<html>Reservas online</html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	f.condoURL = condo.URL

	t.Cleanup(func() { condo.Close(); sso.Close() })
	return f
}

// login builds a client holding these cookies, plus the SSOLogin for it.
func (f *fakeSite) login(t *testing.T, cookies []Cookie) (*Client, *SSOLogin) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.json")
	if cookies != nil {
		data, err := json.Marshal(cookies)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := New(f.condoURL, "1", "2", path)
	if err != nil {
		t.Fatal(err)
	}
	l := NewSSOLogin(c)
	l.SSOHost = f.ssoHost
	return c, l
}

func TestSSOLoginRenewsTheSessionWithoutTheForm(t *testing.T) {
	f := newFakeSite(t, true)
	c, l := f.login(t, []Cookie{
		{Name: "KEYCLOAK_IDENTITY", Value: "alive", Domain: f.ssoHost, Path: "/"},
		{Name: "PHPSESSID", Value: "stale", Domain: mustHost(t, f.condoURL), Path: "/"},
	})

	cookies, err := l.Login(context.Background())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if f.formServed != 0 {
		t.Errorf("the login form was rendered %d times; the point is that it never is", f.formServed)
	}

	got := map[string]string{}
	for _, ck := range cookies {
		got[ck.Name] = ck.Value
	}
	if got["PHPSESSID"] != "fresh" {
		t.Errorf("no fresh app session in %v", cookies)
	}
	// The rotated Keycloak cookie must come back, or the next renewal is stale.
	if got["KEYCLOAK_IDENTITY"] != "rotated" {
		t.Errorf("SSO cookie not carried back: got %q", got["KEYCLOAK_IDENTITY"])
	}

	// Saving is the caller's job; once saved, the next renewal sees the new one.
	if err := c.SetSession(cookies); err != nil {
		t.Fatal(err)
	}
	if v := CookiesForHost(c.Session(), f.ssoHost); len(v) != 1 || v[0].Value != "rotated" {
		t.Errorf("saved SSO cookies are %v, want the rotated one", v)
	}
}

func TestSSOLoginSaysWhenTheKeycloakSessionIsGone(t *testing.T) {
	f := newFakeSite(t, false)
	_, l := f.login(t, []Cookie{
		{Name: "KEYCLOAK_IDENTITY", Value: "stale", Domain: f.ssoHost, Path: "/"},
	})
	if _, err := l.Login(context.Background()); !errors.Is(err, ErrNoSSOSession) {
		t.Fatalf("got %v, want ErrNoSSOSession", err)
	}
}

func TestSSOLoginNeedsKeycloakCookies(t *testing.T) {
	f := newFakeSite(t, true)
	// An app-only session, which is what a half-done dashboard paste leaves.
	_, l := f.login(t, []Cookie{
		{Name: "PHPSESSID", Value: "app", Domain: mustHost(t, f.condoURL), Path: "/"},
	})
	_, err := l.Login(context.Background())
	if !errors.Is(err, ErrNoSSOSession) {
		t.Fatalf("got %v, want ErrNoSSOSession", err)
	}
	if !strings.Contains(err.Error(), f.ssoHost) {
		t.Errorf("error %q should name the host whose cookies are missing", err)
	}
}

func TestSSOLoginIsASessionSource(t *testing.T) {
	// It must satisfy the interface pombo wires it through.
	var _ Login = NewSSOLogin(&Client{})
}

func mustHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}
