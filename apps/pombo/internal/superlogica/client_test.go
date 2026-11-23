package superlogica

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"
)

func TestParseResult(t *testing.T) {
	cases := []struct {
		name string
		body string
		want Result
	}{
		{"booked", `{"status":"200","msg":"Sucesso.","data":[{"status":"200","msg":"Sucesso.","data":{"id_reserva_res":"28609","nm_fila_res":"1"}}]}`,
			Result{Outcome: Booked, ID: "28609", Queue: "1", Msg: "Sucesso."}},
		{"not open", `{"multipleresponse":"1","status":"206","msg":"Todos os 1 itens foram processados COM ERRO.","data":[{"status":"500","msg":"A área ainda não está disponível para reservas."}]}`,
			Result{Outcome: NotOpen, Msg: "A área ainda não está disponível para reservas."}},
		{"already ours", `{"status":"206","data":[{"status":500,"msg":"Essa unidade já reservou nesse dia."}]}`,
			Result{Outcome: AlreadyOurs, Msg: "Essa unidade já reservou nesse dia."}},
		{"taken", `{"status":"206","data":[{"status":"500","msg":"Horário já reservado."}]}`,
			Result{Outcome: Rejected, Msg: "Horário já reservado."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseResult(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
	if _, err := parseResult("<html>login</html>"); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("HTML answer: got %v, want ErrSessionExpired", err)
	}
}

func TestQuoteMatchesPython(t *testing.T) {
	// urllib.parse.quote("BE  CADEIRA 01 - 06h às 08h") and quote("11/23/2026 00:00:00")
	if got := quote("BE  CADEIRA 01 - 06h às 08h"); got != "BE%20%20CADEIRA%2001%20-%2006h%20%C3%A0s%2008h" {
		t.Fatal(got)
	}
	if got := quote("11/23/2026 00:00:00"); got != "11/23/2026%2000%3A00%3A00" {
		t.Fatal(got)
	}
}

func TestBookSendsTheSitesRequest(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/areadocondomino/atual/reservas/put" {
			t.Errorf("path %s", r.URL.Path)
		}
		if c, _ := r.Cookie("PHPSESSID"); c == nil || c.Value != "abc" {
			t.Errorf("session cookie not sent")
		}
		r.ParseForm()
		var payload struct {
			Params []map[string]string `json:"params"`
		}
		if err := json.Unmarshal([]byte(r.PostForm.Get("json")), &payload); err != nil {
			t.Errorf("body: %v", err)
		}
		got = map[string]string{}
		for k, v := range payload.Params[0] {
			got[k], _ = url.PathUnescape(v)
		}
		w.Write([]byte(`{"status":"200","msg":"Sucesso.","data":[{"status":"200","msg":"Sucesso.","data":{"id_reserva_res":"1","nm_fila_res":"1"}}]}`))
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "1", "2", "")
	host, _ := url.Parse(srv.URL)
	c.SetSession([]Cookie{{Name: "PHPSESSID", Value: "abc", Domain: host.Hostname(), Path: "/"}})
	r, err := c.Book(context.Background(), Area{ID: "792", Name: "CADEIRA - 14h às 16h"}, time.Date(2026, 12, 5, 0, 0, 0, 0, time.UTC))
	if err != nil || r.Outcome != Booked {
		t.Fatalf("got %+v, %v", r, err)
	}
	for k, want := range map[string]string{
		"ID_AREA_ARE": "792", "DT_RESERVA_RES": "12/05/2026 00:00:00", "ID_UNIDADE_UNI": "2",
		"ID_CONDOMINIO_COND": "1", "nomeArea": "CADEIRA - 14h às 16h", "FL_REGRAS_ARE": "1",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
}

func TestAliveSpotsTheLoginRedirect(t *testing.T) {
	sso := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<form id=kc-form-login>"))
	}))
	defer sso.Close()
	loggedIn := true
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loggedIn {
			http.Redirect(w, r, sso.URL+"/auth", http.StatusFound)
			return
		}
		w.Write([]byte("<title>Reservas online :: Areadocondomino</title>"))
	}))
	defer site.Close()

	c, _ := New(site.URL, "1", "2", filepath.Join(t.TempDir(), "session.json"))
	if err := c.Alive(context.Background()); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("no session: got %v", err)
	}
	c.SetSession([]Cookie{{Name: "PHPSESSID", Value: "x"}})
	if err := c.Alive(context.Background()); err != nil {
		t.Fatalf("logged in: got %v", err)
	}
	loggedIn = false
	if err := c.Alive(context.Background()); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("redirected: got %v", err)
	}
}

func TestSessionSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	c, _ := New("https://example.test", "1", "2", path)
	if err := c.SetSession([]Cookie{{Name: "PHPSESSID", Value: "x", Domain: "example.test"}}); err != nil {
		t.Fatal(err)
	}
	c2, err := New("https://example.test", "1", "2", path)
	if err != nil || !c2.HasSession() {
		t.Fatalf("session not reloaded: %v", err)
	}
}

type fakeLogin struct {
	cookies []Cookie
	err     error
	calls   int
}

func (f *fakeLogin) Login(context.Context) ([]Cookie, error) { f.calls++; return f.cookies, f.err }

func TestEnsureSessionLogsInOnlyWhenExpired(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, _ := r.Cookie("PHPSESSID"); c != nil && c.Value == "good" {
			w.Write([]byte("Reservas online"))
			return
		}
		w.Write([]byte("login"))
	}))
	defer site.Close()
	c, _ := New(site.URL, "1", "2", "")

	if err := c.EnsureSession(context.Background(), NoLogin{}); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("no login service: got %v", err)
	}
	l := &fakeLogin{cookies: []Cookie{{Name: "PHPSESSID", Value: "good"}}}
	if err := c.EnsureSession(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureSession(context.Background(), l); err != nil || l.calls != 1 {
		t.Fatalf("logged in again although the session works: calls=%d err=%v", l.calls, err)
	}
}
