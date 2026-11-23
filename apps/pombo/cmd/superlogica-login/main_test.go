package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/superlogica"
)

func TestWaitForCodePollsUntilTheEmailArrives(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.Contains(r.URL.RawQuery, "since=") {
			t.Errorf("no since in %s", r.URL)
		}
		if calls.Add(1) < 2 {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"no email yet"}`))
			return
		}
		w.Write([]byte(`{"code":"482913"}`))
	}))
	defer srv.Close()

	cfg := config{OTPURL: srv.URL, OTPToken: "tok", OTPTimeout: 10 * time.Second}
	code, err := waitForCode(context.Background(), cfg, time.Now())
	if err != nil || code != "482913" {
		t.Fatalf("got %q, %v", code, err)
	}
	cfg.OTPToken = "wrong"
	if _, err := waitForCode(context.Background(), cfg, time.Now()); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("bad token: got %v", err)
	}
}

// writeFile saves test input and returns its path.
func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readSeeded(t *testing.T, path string) []superlogica.Cookie {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cookies []superlogica.Cookie
	if err := json.Unmarshal(data, &cookies); err != nil {
		t.Fatal(err)
	}
	return cookies
}

func TestSeedSessionReadsACopyAsCurlCommand(t *testing.T) {
	dir := t.TempDir()
	to := filepath.Join(dir, "session.json")
	from := writeFile(t, dir, "curl.txt", `curl 'https://sso.superlogica.com/auth/realms/superlogica-cloud/account/' \
  -H 'User-Agent: Mozilla/5.0' \
  -H 'Cookie: AUTH_SESSION_ID=abc; KEYCLOAK_IDENTITY=id-token; KEYCLOAK_SESSION=realm/user/sess' \
  -H 'Accept-Language: pt-BR'`)

	n, err := seedSession(from, to, "sso.superlogica.com")
	if err != nil || n != 3 {
		t.Fatalf("got %d, %v; want 3 cookies", n, err)
	}
	got := map[string]string{}
	for _, ck := range readSeeded(t, to) {
		got[ck.Name] = ck.Value
		if ck.Domain != "sso.superlogica.com" {
			t.Errorf("%s got domain %q", ck.Name, ck.Domain)
		}
	}
	// A value containing "/" must survive intact.
	if got["KEYCLOAK_SESSION"] != "realm/user/sess" || got["KEYCLOAK_IDENTITY"] != "id-token" {
		t.Errorf("cookies came out wrong: %v", got)
	}
}

func TestSeedSessionRejectsACurlForTheWrongHost(t *testing.T) {
	dir := t.TempDir()
	// The condo site never sees the Keycloak cookies; this is the easy mistake.
	from := writeFile(t, dir, "curl.txt",
		`curl 'https://kpgadministrado.superlogica.net/areadocondomino/atual/cobranca/index' `+
			`-H 'Cookie: PHPSESSID=app; server-id=3'`)

	_, err := seedSession(from, filepath.Join(dir, "session.json"), "sso.superlogica.com")
	if err == nil {
		t.Fatal("want an error naming the wrong host")
	}
	for _, want := range []string{"kpgadministrado.superlogica.net", "sso.superlogica.com"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q doesn't mention %s", err, want)
		}
	}
}

func TestSeedSessionKeepsOnlyTheKeycloakCookiesFromJSON(t *testing.T) {
	dir := t.TempDir()
	to := filepath.Join(dir, "session.json")
	from := writeFile(t, dir, "browser.json", `[
		{"name":"PHPSESSID","value":"app","domain":"kpgadministrado.superlogica.net","path":"/"},
		{"name":"KEYCLOAK_IDENTITY","value":"id","domain":"sso.superlogica.com","path":"/"},
		{"name":"KEYCLOAK_SESSION","value":"sess","domain":".sso.superlogica.com","path":"/"}
	]`)

	n, err := seedSession(from, to, "sso.superlogica.com")
	if err != nil || n != 2 {
		t.Fatalf("got %d, %v; want 2 cookies", n, err)
	}
	for _, ck := range readSeeded(t, to) {
		if ck.Name == "PHPSESSID" {
			t.Error("the app session was seeded as an SSO cookie")
		}
	}

	// A browser export of only the condo domain is a mistake worth naming.
	onlyApp := writeFile(t, dir, "onlyapp.json",
		`[{"name":"PHPSESSID","value":"app","domain":"kpgadministrado.superlogica.net","path":"/"}]`)
	if _, err := seedSession(onlyApp, to, "sso.superlogica.com"); err == nil || !strings.Contains(err.Error(), "both domains") {
		t.Errorf("got %v, want a message about exporting both domains", err)
	}
}

func TestSeedSessionComplainsAboutACurlWithNoCookies(t *testing.T) {
	dir := t.TempDir()
	from := writeFile(t, dir, "curl.txt", `curl 'https://sso.superlogica.com/auth/' -H 'Accept: */*'`)
	if _, err := seedSession(from, filepath.Join(dir, "session.json"), "sso.superlogica.com"); err == nil || !strings.Contains(err.Error(), "Cookie header") {
		t.Errorf("got %v, want a complaint about the missing Cookie header", err)
	}
}
