package notify

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/settings"
)

type fakeWA struct{ sent map[string]string }

func (f *fakeWA) Send(_ context.Context, jid, text string) error {
	f.sent[jid] = text
	return nil
}

type fakeRoutes []settings.Route

func (f fakeRoutes) Routes(context.Context) ([]settings.Route, error) { return f, nil }

func TestNotifySendsToDocumentsGroups(t *testing.T) {
	wa := &fakeWA{sent: map[string]string{}}
	routes := fakeRoutes{{GroupJID: "docs@g.us", Handler: "documents"}, {GroupJID: "family@g.us", Handler: ""}}
	h := New(wa, routes, "secret", "documents", slog.New(slog.NewTextHandler(io.Discard, nil)))

	body := `{"document_id":8,"title":"CNH do André","summary":"Categoria AB, válida até 2031.","sent_by":"Luana","url":"https://docs.example/documents/8/details"}`
	req := httptest.NewRequest(http.MethodPost, "/notify", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	want := "📄 *CNH do André*\nCategoria AB, válida até 2031.\nEnviado por Luana · https://docs.example/documents/8/details"
	if len(wa.sent) != 1 || wa.sent["docs@g.us"] != want {
		t.Fatalf("sent = %q", wa.sent)
	}
}

func TestNotifyRejectsWrongToken(t *testing.T) {
	for _, token := range []string{"secret", ""} {
		h := New(&fakeWA{}, fakeRoutes{}, token, "documents", slog.Default())
		req := httptest.NewRequest(http.MethodPost, "/notify", strings.NewReader(`{"title":"x"}`))
		req.Header.Set("Authorization", "Bearer nope")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("token %q: status %d", token, rec.Code)
		}
	}
}

func TestTextWithoutSummaryOrSender(t *testing.T) {
	if got := Text(Document{Title: "conta.pdf", URL: "https://x/1"}); got != "📄 *conta.pdf*\nhttps://x/1" {
		t.Fatalf("got %q", got)
	}
}
