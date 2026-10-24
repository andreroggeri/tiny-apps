package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/andreroggeri/tiny-apps/apps/coruja/internal/llm"
	"github.com/andreroggeri/tiny-apps/apps/coruja/internal/paperless"
)

const (
	summaryField = 3
	sentByField  = 4
)

type fakePaperless struct {
	doc     paperless.Document
	updates []map[string]any
	shared  []string
	waited  bool
}

func (f *fakePaperless) Document(context.Context, int) (paperless.Document, error) { return f.doc, nil }
func (f *fakePaperless) Original(context.Context, int) ([]byte, error)             { return []byte("jpeg"), nil }
func (f *fakePaperless) WaitForAI(context.Context, int, time.Duration) error {
	f.waited = true
	return nil
}
func (f *fakePaperless) Update(_ context.Context, _ int, patch map[string]any) error {
	f.updates = append(f.updates, patch)
	return nil
}
func (f *fakePaperless) Share(_ context.Context, kind string, id int) (bool, error) {
	f.shared = append(f.shared, kind)
	return true, nil
}

type fakeLLM struct {
	got llm.Document
	err error
}

func (f *fakeLLM) Summarize(_ context.Context, d llm.Document) (llm.Summary, error) {
	f.got = d
	return llm.Summary{Title: "CNH do André", Summary: "Carteira de motorista, categoria AB, válida até 2031."}, f.err
}

type fakeNotifier struct{ got []Notification }

func (f *fakeNotifier) Notify(_ context.Context, n Notification) error {
	f.got = append(f.got, n)
	return nil
}

func newApp(p *fakePaperless, l *fakeLLM, n *fakeNotifier) *App {
	a := New(p, l, n, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.Token, a.SummaryField, a.SentByField = "secret", summaryField, sentByField
	a.PublicURL = "https://docs.example/"
	return a
}

func cnh() paperless.Document {
	five := 5
	return paperless.Document{
		ID: 8, Title: "whatsapp-20261024-133000", OriginalFileName: "whatsapp-20261024-133000.jpg",
		MimeType: "image/jpeg", Content: "HABILITAÇÃO", Tags: []int{1, 2}, DocumentType: &five,
		CustomFields: []paperless.FieldValue{{Field: sentByField, Value: "Luana"}},
	}
}

func TestProcessSummarizesSharesAndNotifies(t *testing.T) {
	p, l, n := &fakePaperless{doc: cnh()}, &fakeLLM{}, &fakeNotifier{}
	if err := newApp(p, l, n).Process(context.Background(), 8); err != nil {
		t.Fatal(err)
	}
	if !p.waited {
		t.Error("did not wait for paperless' AI")
	}
	if string(l.got.Image) != "jpeg" || l.got.Text != "HABILITAÇÃO" {
		t.Errorf("LLM got %+v", l.got)
	}
	want := map[string]any{
		"title": "CNH do André",
		"custom_fields": []paperless.FieldValue{
			{Field: sentByField, Value: "Luana"},
			{Field: summaryField, Value: "Carteira de motorista, categoria AB, válida até 2031."},
		},
	}
	if len(p.updates) != 1 || !reflect.DeepEqual(p.updates[0], want) {
		t.Errorf("updates = %+v", p.updates)
	}
	if strings.Join(p.shared, ",") != "tags,tags,document_types" && strings.Join(p.shared, ",") != "document_types,tags,tags" {
		t.Errorf("shared = %v", p.shared)
	}
	wantN := Notification{
		DocumentID: 8, Title: "CNH do André", Summary: "Carteira de motorista, categoria AB, válida até 2031.",
		SentBy: "Luana", URL: "https://docs.example/documents/8/details",
	}
	if len(n.got) != 1 || n.got[0] != wantN {
		t.Errorf("notifications = %+v", n.got)
	}
}

func TestProcessKeepsAChosenTitle(t *testing.T) {
	doc := cnh()
	doc.Title = "Carteira de habilitação"
	p, n := &fakePaperless{doc: doc}, &fakeNotifier{}
	newApp(p, &fakeLLM{}, n).Process(context.Background(), 8)
	if _, ok := p.updates[0]["title"]; ok {
		t.Errorf("overwrote the title: %+v", p.updates[0])
	}
	if n.got[0].Title != "Carteira de habilitação" {
		t.Errorf("notification title = %q", n.got[0].Title)
	}
}

func TestProcessSkipsSummarizedDocument(t *testing.T) {
	doc := cnh()
	doc.CustomFields = append(doc.CustomFields, paperless.FieldValue{Field: summaryField, Value: "já feito"})
	p, n := &fakePaperless{doc: doc}, &fakeNotifier{}
	newApp(p, &fakeLLM{}, n).Process(context.Background(), 8)
	if len(p.updates) != 0 || len(n.got) != 0 {
		t.Errorf("repeated work: updates %v, notifications %v", p.updates, n.got)
	}
}

func TestProcessNotifiesEvenWhenLLMFails(t *testing.T) {
	p, n := &fakePaperless{doc: cnh()}, &fakeNotifier{}
	newApp(p, &fakeLLM{err: errors.New("down")}, n).Process(context.Background(), 8)
	if len(p.updates) != 0 {
		t.Errorf("wrote %v without a summary", p.updates)
	}
	if len(n.got) != 1 || n.got[0].Summary != "" || n.got[0].Title != "whatsapp-20261024-133000" {
		t.Errorf("notifications = %+v", n.got)
	}
}

func TestWebhook(t *testing.T) {
	a := newApp(&fakePaperless{}, &fakeLLM{}, nil)
	tests := []struct {
		name, auth, body string
		want             int
	}{
		{"string id", "Bearer secret", `{"doc_id":"12"}`, http.StatusAccepted},
		{"number id", "Bearer secret", `{"doc_id":12}`, http.StatusAccepted},
		{"wrong token", "Bearer nope", `{"doc_id":"12"}`, http.StatusUnauthorized},
		{"no id", "Bearer secret", `{}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(tt.body))
			req.Header.Set("Authorization", tt.auth)
			rec := httptest.NewRecorder()
			a.Handler().ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d", rec.Code, tt.want)
			}
		})
	}
	if len(a.queue) != 2 || <-a.queue != 12 {
		t.Errorf("queue has %d documents", len(a.queue))
	}
}
