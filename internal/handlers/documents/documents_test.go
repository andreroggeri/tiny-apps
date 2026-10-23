package documents

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/andreroggeri/pombo/internal/paperless"
	"github.com/andreroggeri/pombo/internal/router"
)

type fakePaperless struct {
	uploadedName string
	initialTitle string
	consumeErr   error
}

func (f *fakePaperless) Upload(_ context.Context, name string, _ []byte) (string, error) {
	f.uploadedName = name
	return "task-1", nil
}

func (f *fakePaperless) WaitForDocument(context.Context, string) (int, error) {
	return 7, f.consumeErr
}

func (f *fakePaperless) WaitForTitleChange(_ context.Context, id int, initial string) (paperless.Document, error) {
	f.initialTitle = initial
	return paperless.Document{ID: id, Title: "Fatura Enel", Tags: []string{"Contas", "Casa"}, Correspondent: "Enel"}, nil
}

func newHandler(p Paperless) *Handler {
	h := New(p, "https://docs.example/")
	h.now = func() time.Time { return time.Date(2026, 10, 23, 15, 4, 5, 0, time.UTC) }
	return h
}

func photo() *router.Media {
	return &router.Media{
		MimeType: "image/jpeg",
		Download: func(context.Context) ([]byte, error) { return []byte("jpeg"), nil },
	}
}

func TestPhotoIsFiledAndSummarised(t *testing.T) {
	p := &fakePaperless{}
	var replies []string
	msg := router.Message{
		Media:     photo(),
		ReplyFunc: func(_ context.Context, text string) error { replies = append(replies, text); return nil },
	}
	if err := newHandler(p).Handle(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	if p.uploadedName != "whatsapp-20261023-150405.jpg" {
		t.Errorf("uploaded as %q", p.uploadedName)
	}
	if p.initialTitle != "whatsapp-20261023-150405" {
		t.Errorf("waited for title change from %q", p.initialTitle)
	}
	if len(replies) != 2 {
		t.Fatalf("got %d replies, want acknowledgement + summary: %q", len(replies), replies)
	}
	want := "✅ Arquivado: *Fatura Enel*\n👤 Enel\n🏷️ Contas, Casa\n🔗 https://docs.example/documents/7/details"
	if replies[1] != want {
		t.Errorf("summary =\n%s\nwant\n%s", replies[1], want)
	}
}

func TestDocumentKeepsItsName(t *testing.T) {
	p := &fakePaperless{}
	media := photo()
	media.FileName, media.MimeType = "contrato aluguel.pdf", "application/pdf"
	if err := newHandler(p).Handle(context.Background(), router.Message{Media: media}); err != nil {
		t.Fatal(err)
	}
	if p.uploadedName != "contrato aluguel.pdf" || p.initialTitle != "contrato aluguel" {
		t.Errorf("uploaded %q, initial title %q", p.uploadedName, p.initialTitle)
	}
}

func TestTextMessagesAreIgnored(t *testing.T) {
	p := &fakePaperless{}
	if err := newHandler(p).Handle(context.Background(), router.Message{Text: "oi"}); err != nil {
		t.Fatal(err)
	}
	if p.uploadedName != "" {
		t.Error("text message should not upload anything")
	}
}

func TestConsumeFailureIsReported(t *testing.T) {
	p := &fakePaperless{consumeErr: paperless.ErrConsumeFailed}
	err := newHandler(p).Handle(context.Background(), router.Message{Media: photo()})
	if !errors.Is(err, paperless.ErrConsumeFailed) || !strings.Contains(err.Error(), "paperless") {
		t.Fatalf("err = %v", err)
	}
}
