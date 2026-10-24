package dash

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andreroggeri/pombo/internal/settings"
	"github.com/andreroggeri/pombo/internal/whatsapp"
)

type fakeWA struct{ status whatsapp.Status }

func (f fakeWA) Status() whatsapp.Status { return f.status }
func (f fakeWA) Groups(context.Context) ([]whatsapp.Group, error) {
	return []whatsapp.Group{{JID: "docs@g.us", Name: "Documentos"}}, nil
}

func newServer(t *testing.T, status whatsapp.Status) (*settings.Store, http.Handler) {
	t.Helper()
	store, err := settings.Open(context.Background(), filepath.Join(t.TempDir(), "pombo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	s := New(fakeWA{status}, store, []string{"documents"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return store, s.Handler()
}

func post(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRouteAndSenderForms(t *testing.T) {
	store, h := newServer(t, whatsapp.Status{LoggedIn: true, Connected: true, Phone: "5511900000000"})
	ctx := context.Background()

	rec := post(t, h, "/routes", url.Values{"group_jid": {"docs@g.us"}, "group_name": {"Documentos"}, "handler": {"documents"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("set route: %d %s", rec.Code, rec.Body)
	}
	if got, _ := store.HandlerFor(ctx, "docs@g.us"); got != "documents" {
		t.Fatalf("route = %q", got)
	}

	if rec := post(t, h, "/routes", url.Values{"group_jid": {"docs@g.us"}, "handler": {"rm -rf"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown handler accepted: %d", rec.Code)
	}

	post(t, h, "/senders", url.Values{"name": {"André"}, "phone": {"+55 (11) 99999-8888"}})
	if ok, _ := store.IsAllowed(ctx, "5511999998888"); !ok {
		t.Fatal("sender not stored with a normalised phone")
	}
	post(t, h, "/senders/delete", url.Values{"phone": {"5511999998888"}})
	if ok, _ := store.IsAllowed(ctx, "5511999998888"); ok {
		t.Fatal("sender not removed")
	}
}

func TestIndexShowsGroupsAndStatus(t *testing.T) {
	_, h := newServer(t, whatsapp.Status{LoggedIn: true, Connected: true, Phone: "5511900000000"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	for _, want := range []string{"Conectado", "+5511900000000", "Documentos", "docs@g.us"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestIndexShowsQRWhenNotPaired(t *testing.T) {
	_, h := newServer(t, whatsapp.Status{QRCode: "2@abc,def,ghi"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), "data:image/png;base64,") {
		t.Fatal("QR image not rendered")
	}
}
