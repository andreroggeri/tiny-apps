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
	uploadErr error
}

func (f *fakePaperless) Upload(_ context.Context, name string, data []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploads = append(f.uploads, upload{name, data})
	return "task", f.uploadErr
}

func (f *fakePaperless) WaitForDocument(context.Context, string) (int, error) { return 7, nil }

func (f *fakePaperless) AddNote(_ context.Context, _ int, note string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, note)
	return nil
}

// fakeGrouper returns fixed groups and records what it was asked.
type fakeGrouper struct {
	groups  [][]int
	err     error
	pages   []grouping.Page
	context string
}

func (g *fakeGrouper) Group(_ context.Context, pages []grouping.Page, batchContext string) ([][]int, error) {
	g.pages, g.context = pages, batchContext
	return g.groups, g.err
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
	g := &fakeGrouper{groups: [][]int{{0, 1}, {2}}}
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
	if len(g.pages) != 3 || g.context != "contrato do apartamento" || g.pages[2].Caption != "recibo do mercado" {
		t.Errorf("grouper got %d pages, context %q", len(g.pages), g.context)
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
