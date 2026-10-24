package documents

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andreroggeri/pombo/internal/grouping"
	"github.com/andreroggeri/pombo/internal/router"
)

type upload struct {
	name string
	data []byte
}

type fakePaperless struct {
	mu        sync.Mutex
	uploads   []upload
	notes     []string
	deleted   []int
	uploadErr error
}

func (f *fakePaperless) Upload(_ context.Context, name string, data []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploads = append(f.uploads, upload{name, data})
	return "task", f.uploadErr
}

// WaitForDocument numbers documents by upload: the first is 1.
func (f *fakePaperless) WaitForDocument(context.Context, string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.uploads), nil
}

func (f *fakePaperless) AddNote(_ context.Context, _ int, note string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, note)
	return nil
}

func (f *fakePaperless) Content(context.Context, int) (string, error) { return "", nil }

func (f *fakePaperless) Delete(_ context.Context, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}

// fakeGrouper returns fixed documents and records what it was asked.
type fakeGrouper struct {
	docs    []grouping.Document
	err     error
	same    float64 // SameDocument's answer
	entries []grouping.Entry
	asked   []grouping.Recent // SameDocument calls
}

func (g *fakeGrouper) Group(_ context.Context, entries []grouping.Entry) ([]grouping.Document, error) {
	g.entries = entries
	return g.docs, g.err
}

func (g *fakeGrouper) SameDocument(_ context.Context, r grouping.Recent, _ grouping.Entry) (float64, error) {
	g.asked = append(g.asked, r)
	return g.same, nil
}

// chat records reactions and replies per message.
type chat struct {
	mu        sync.Mutex
	reactions map[string][]string
	replies   []string
}

func (c *chat) msg(id, text string, media *router.Media) router.Message {
	return router.Message{
		ChatJID: "docs@g.us", SenderPhone: "5511999998888", Text: text, Media: media,
		ReplyFunc: func(_ context.Context, t string) error {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.replies = append(c.replies, t)
			return nil
		},
		ReactFunc: func(_ context.Context, e string) error {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.reactions == nil {
				c.reactions = map[string][]string{}
			}
			c.reactions[id] = append(c.reactions[id], e)
			return nil
		},
	}
}

func (c *chat) last(id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.reactions[id]
	if len(r) == 0 {
		return ""
	}
	return r[len(r)-1]
}

func jpegPhoto(t *testing.T) *router.Media {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 60))
	for x := 0; x < 40; x++ {
		img.Set(x, x, color.Black)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	return &router.Media{MimeType: "image/jpeg", Download: func(context.Context) ([]byte, error) { return data, nil }}
}

func newHandler(p Paperless, g Grouper) *Handler {
	h := New(p, g, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.BatchWindow = 20 * time.Millisecond
	h.now = func() time.Time { return time.Date(2026, 10, 23, 15, 4, 5, 0, time.UTC) }
	return h
}

func pdfPages(data []byte) int {
	return bytes.Count(data, []byte("/Type /Page\n"))
}

func TestLonePhotoIsUploadedAsIs(t *testing.T) {
	p, c := &fakePaperless{}, &chat{}
	h := newHandler(p, &fakeGrouper{})
	if err := h.Handle(context.Background(), c.msg("1", "", jpegPhoto(t))); err != nil {
		t.Fatal(err)
	}
	if c.last("1") != reactWorking {
		t.Fatalf("expected ⏳ right away, got %q", c.reactions["1"])
	}
	h.Wait()

	if len(p.uploads) != 1 || p.uploads[0].name != "whatsapp-20261023-150405.jpg" {
		t.Fatalf("uploads = %+v", p.uploads)
	}
	if c.last("1") != reactDone || len(c.replies) != 0 {
		t.Fatalf("want ✅ and no reply, got reactions %q replies %q", c.reactions["1"], c.replies)
	}
}

func TestBatchIsGroupedIntoDocumentsWithContext(t *testing.T) {
	p, c := &fakePaperless{}, &chat{}
	g := &fakeGrouper{docs: []grouping.Document{
		{Pages: []int{0, 1}, Context: "contrato do apartamento"},
		{Pages: []int{2}, Context: "recibo do mercado"},
	}}
	h := newHandler(p, g)
	ctx := context.Background()

	// Context first, then three photos sent one by one: a two-page contract
	// and a receipt.
	h.Handle(ctx, c.msg("t", "contrato do apartamento", nil))
	h.Handle(ctx, c.msg("1", "", jpegPhoto(t)))
	h.Handle(ctx, c.msg("2", "", jpegPhoto(t)))
	h.Handle(ctx, c.msg("3", "recibo do mercado", jpegPhoto(t)))
	h.Wait()

	if c.last("t") != reactNoted {
		t.Errorf("context message reaction = %q, want 📝", c.reactions["t"])
	}
	// The grouper sees the whole timeline, in order.
	if len(g.entries) != 4 || g.entries[0].Text != "contrato do apartamento" || g.entries[0].Image != nil ||
		g.entries[3].Text != "recibo do mercado" || g.entries[3].Image == nil {
		t.Errorf("grouper got entries %+v", g.entries)
	}
	if len(p.uploads) != 2 {
		t.Fatalf("want 2 documents, got %d", len(p.uploads))
	}
	// Two photo pages + one context page; one photo page + one context page.
	if n := pdfPages(p.uploads[0].data); n != 3 {
		t.Errorf("contract PDF has %d pages, want 3", n)
	}
	if n := pdfPages(p.uploads[1].data); n != 2 {
		t.Errorf("receipt PDF has %d pages, want 2", n)
	}
	for _, id := range []string{"1", "2", "3"} {
		if c.last(id) != reactDone {
			t.Errorf("photo %s reactions = %q, want ending in ✅", id, c.reactions[id])
		}
	}
}

func TestEmptyLLMContextFallsBackToCaptions(t *testing.T) {
	g := &fakeGrouper{docs: []grouping.Document{{Pages: []int{0}}, {Pages: []int{1}}}}
	h := newHandler(&fakePaperless{}, g)
	c := &chat{}
	entries := []item{
		{msg: c.msg("1", "", nil), data: []byte("a"), mime: "image/jpeg", text: "frente"},
		{msg: c.msg("2", "", nil), data: []byte("b"), mime: "image/jpeg"},
	}
	docs := h.group(context.Background(), entries, positionalContext(entries))
	if len(docs) != 2 || docs[0].context != "frente" || docs[1].context != "" {
		t.Fatalf("docs = %+v", docs)
	}
}

func TestPositionalContext(t *testing.T) {
	photo := func(caption string) item { return item{data: []byte("x"), mime: "image/jpeg", text: caption} }
	txt := func(s string) item { return item{text: s} }
	tests := []struct {
		name    string
		entries []item
		want    []string
	}{
		{
			// The side note's scenario: each text belongs to the photo after it.
			"text before each photo",
			[]item{txt("minha CNH"), photo(""), txt("carteira de vacinação"), photo("")},
			[]string{"", "minha CNH", "", "carteira de vacinação"},
		},
		{"text after the photo", []item{photo(""), txt("minha CNH")}, []string{"minha CNH", ""}},
		{
			"trailing text goes to the photos just before it",
			[]item{photo("a"), txt("contrato"), photo(""), photo(""), txt("assinado")},
			[]string{"a", "", "contrato\nassinado", "contrato\nassinado", ""},
		},
		{"caption stays with its photo", []item{photo("frente"), photo("verso")}, []string{"frente", "verso"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := positionalContext(tt.entries); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTextWithoutPhotoCarriesToNextBatch(t *testing.T) {
	p, c := &fakePaperless{}, &chat{}
	h := newHandler(p, nil)
	ctx := context.Background()
	h.Handle(ctx, c.msg("t", "vou mandar a CNH", nil))
	h.Wait()
	h.Handle(ctx, c.msg("1", "", jpegPhoto(t)))
	h.Wait()

	// With context the photo becomes a PDF with a context page.
	if len(p.uploads) != 1 || !strings.HasSuffix(p.uploads[0].name, ".pdf") {
		t.Fatalf("uploads = %+v", p.uploads)
	}
}

func TestOldTextIsForgotten(t *testing.T) {
	p, c := &fakePaperless{}, &chat{}
	h := newHandler(p, nil)
	ctx := context.Background()
	now := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	h.Handle(ctx, c.msg("t", "bom dia", nil))
	h.Wait()
	now = now.Add(h.MergeWindow + time.Minute)
	h.Handle(ctx, c.msg("1", "", jpegPhoto(t)))
	h.Wait()

	if len(p.uploads) != 1 || !strings.HasSuffix(p.uploads[0].name, ".jpg") {
		t.Fatalf("uploads = %+v", p.uploads)
	}
}

func TestLaterPhotoIsAddedToRecentDocument(t *testing.T) {
	p, c := &fakePaperless{}, &chat{}
	g := &fakeGrouper{same: 0.95}
	h := newHandler(p, g)
	ctx := context.Background()

	h.Handle(ctx, c.msg("1", "", jpegPhoto(t))) // document 1: the front
	h.Wait()
	if len(g.asked) != 0 {
		t.Fatalf("nothing to compare with yet, but SameDocument was asked %d times", len(g.asked))
	}
	h.Handle(ctx, c.msg("2", "", jpegPhoto(t))) // minutes later: the back
	h.Wait()

	// Document 1 had no OCR text, so it's compared by its last page.
	if len(g.asked) != 1 || g.asked[0].Image == nil {
		t.Fatalf("SameDocument asked %+v", g.asked)
	}
	if len(p.uploads) != 2 || pdfPages(p.uploads[1].data) != 2 {
		t.Fatalf("want a 2-page replacement, got %+v", p.uploads)
	}
	if !reflect.DeepEqual(p.deleted, []int{1}) {
		t.Errorf("deleted = %v, want [1]", p.deleted)
	}
	if c.last("2") != reactDone {
		t.Errorf("reactions = %q", c.reactions["2"])
	}

	// The replacement is now the only document new photos can join, and
	// it has both pages: a third photo joins it, replacing document 2.
	h.Handle(ctx, c.msg("3", "", jpegPhoto(t)))
	h.Wait()
	if len(g.asked) != 2 || pdfPages(p.uploads[2].data) != 3 || !reflect.DeepEqual(p.deleted, []int{1, 2}) {
		t.Errorf("asked %d times, deleted %v", len(g.asked), p.deleted)
	}
}

func TestUnsureContinuationIsFiledSeparately(t *testing.T) {
	p, c := &fakePaperless{}, &chat{}
	g := &fakeGrouper{same: 0.6}
	h := newHandler(p, g)
	ctx := context.Background()

	h.Handle(ctx, c.msg("1", "", jpegPhoto(t)))
	h.Wait()
	h.Handle(ctx, c.msg("2", "", jpegPhoto(t)))
	h.Wait()

	if len(p.uploads) != 2 || !strings.HasSuffix(p.uploads[1].name, ".jpg") || len(p.deleted) != 0 {
		t.Fatalf("uploads %+v, deleted %v", p.uploads, p.deleted)
	}
}

func TestGroupingFailureFilesEachPhotoSeparately(t *testing.T) {
	p, c := &fakePaperless{}, &chat{}
	h := newHandler(p, &fakeGrouper{err: errors.New("llm down")})
	ctx := context.Background()
	h.Handle(ctx, c.msg("1", "", jpegPhoto(t)))
	h.Handle(ctx, c.msg("2", "", jpegPhoto(t)))
	h.Wait()
	if len(p.uploads) != 2 {
		t.Fatalf("want 2 separate documents, got %d", len(p.uploads))
	}
}

func TestFileGetsContextAsNote(t *testing.T) {
	p, c := &fakePaperless{}, &chat{}
	h := newHandler(p, nil)
	media := jpegPhoto(t)
	media.FileName, media.MimeType = "cnh.pdf", "application/pdf"
	h.Handle(context.Background(), c.msg("1", "minha CNH", media))
	h.Wait()

	if len(p.uploads) != 1 || p.uploads[0].name != "cnh.pdf" {
		t.Fatalf("uploads = %+v", p.uploads)
	}
	if len(p.notes) != 1 || !strings.Contains(p.notes[0], "minha CNH") {
		t.Fatalf("notes = %q", p.notes)
	}
}

func TestTextOnlyBatchFilesNothing(t *testing.T) {
	p, c := &fakePaperless{}, &chat{}
	h := newHandler(p, nil)
	h.Handle(context.Background(), c.msg("t", "bom dia", nil))
	h.Wait()
	if len(p.uploads) != 0 {
		t.Fatal("a text-only batch should not upload anything")
	}
}

func TestUploadFailureReactsAndExplains(t *testing.T) {
	p, c := &fakePaperless{uploadErr: errors.New("paperless is down")}, &chat{}
	h := newHandler(p, nil)
	h.Handle(context.Background(), c.msg("1", "", jpegPhoto(t)))
	h.Wait()
	if c.last("1") != reactFailed {
		t.Errorf("reactions = %q, want ending in ❌", c.reactions["1"])
	}
	if len(c.replies) != 1 || !strings.Contains(c.replies[0], "paperless is down") {
		t.Errorf("replies = %q", c.replies)
	}
}
