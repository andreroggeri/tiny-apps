// Package documents files what's sent to the documents group in paperless.
//
// Photos sent close together are collected into a batch, grouped into
// documents by a vision LLM (so a multi-page contract becomes one PDF), and
// anything the sender wrote - a caption or a text message in the same batch,
// like "carteira de vacinação do bebê" - is added to the document so
// paperless' AI and search can use it. Progress is shown with reactions.
package documents

import (
	"context"
	"fmt"
	"log/slog"
	"mime"
	"strings"
	"sync"
	"time"

	"github.com/andreroggeri/pombo/internal/grouping"
	"github.com/andreroggeri/pombo/internal/pdf"
	"github.com/andreroggeri/pombo/internal/router"
)

// Name is the handler name shown in the dashboard.
const Name = "documents"

const (
	reactWorking = "⏳"
	reactDone    = "✅"
	reactFailed  = "❌"
	reactNoted   = "📝"
)

type Paperless interface {
	Upload(ctx context.Context, filename string, data []byte) (string, error)
	WaitForDocument(ctx context.Context, taskID string) (int, error)
	AddNote(ctx context.Context, id int, note string) error
}

type Grouper interface {
	Group(ctx context.Context, pages []grouping.Page, batchContext string) ([][]int, error)
}

// item is one photo or file in a batch, already downloaded.
type item struct {
	msg     router.Message
	data    []byte
	mime    string
	name    string
	caption string
}

type batch struct {
	items    []item
	contexts []string // text messages sent during the batch
	timer    *time.Timer
}

type batchKey struct{ chat, sender string }

type Handler struct {
	paperless Paperless
	grouper   Grouper // nil: every photo becomes its own document
	log       *slog.Logger

	// BatchWindow is how long to wait after the last photo or text before
	// filing the batch. ConsumeTimeout bounds OCR + consumption per document.
	BatchWindow    time.Duration
	ConsumeTimeout time.Duration
	now            func() time.Time

	mu      sync.Mutex
	batches map[batchKey]*batch
	wg      sync.WaitGroup // open batches, for tests
}

func New(p Paperless, g Grouper, log *slog.Logger) *Handler {
	return &Handler{
		paperless:      p,
		grouper:        g,
		log:            log,
		BatchWindow:    30 * time.Second,
		ConsumeTimeout: 10 * time.Minute,
		now:            time.Now,
		batches:        map[batchKey]*batch{},
	}
}

// Handle adds the message to the sender's batch and returns; the batch is
// filed once the sender has been quiet for BatchWindow.
func (h *Handler) Handle(ctx context.Context, m router.Message) error {
	key := batchKey{m.ChatJID, m.SenderPhone}
	text := strings.TrimSpace(m.Text)

	if m.Media == nil {
		if text == "" {
			return nil
		}
		h.add(key, nil, text)
		return m.React(ctx, reactNoted)
	}

	// Download now: WhatsApp media links expire, and the batch may wait.
	data, err := m.Media.Download(ctx)
	if err != nil {
		return fmt.Errorf("baixar o arquivo do WhatsApp: %w", err)
	}
	it := item{msg: m, data: data, mime: m.Media.MimeType, name: m.Media.FileName, caption: text}
	h.add(key, &it, "")
	return m.React(ctx, reactWorking)
}

func (h *Handler) add(key batchKey, it *item, context string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok := h.batches[key]
	if !ok {
		b = &batch{}
		h.batches[key] = b
		h.wg.Add(1)
		b.timer = time.AfterFunc(h.BatchWindow, func() { h.flush(key) })
	} else {
		b.timer.Reset(h.BatchWindow)
	}
	if it != nil {
		b.items = append(b.items, *it)
	}
	if context != "" {
		b.contexts = append(b.contexts, context)
	}
}

// Wait blocks until all open batches have been filed. For tests.
func (h *Handler) Wait() { h.wg.Wait() }

func (h *Handler) flush(key batchKey) {
	defer h.wg.Done()
	h.mu.Lock()
	b := h.batches[key]
	delete(h.batches, key)
	h.mu.Unlock()
	if b == nil || len(b.items) == 0 {
		return // only text, no photos: just chat
	}

	ctx := context.Background()
	batchContext := strings.Join(b.contexts, "\n")

	var photos, files []item
	for _, it := range b.items {
		if pdf.Supported(it.mime) && it.name == "" {
			photos = append(photos, it)
		} else {
			files = append(files, it)
		}
	}

	for _, group := range h.group(ctx, photos, batchContext) {
		h.filePhotos(ctx, group, batchContext)
	}
	for _, f := range files {
		h.fileAsIs(ctx, f, batchContext)
	}
}

// group splits photos into documents, falling back to one document per photo
// when there's no grouper or it fails: merging later in paperless is easy.
func (h *Handler) group(ctx context.Context, photos []item, batchContext string) [][]item {
	single := func() [][]item {
		out := make([][]item, len(photos))
		for i, p := range photos {
			out[i] = []item{p}
		}
		return out
	}
	if len(photos) < 2 || h.grouper == nil {
		return single()
	}
	pages := make([]grouping.Page, len(photos))
	for i, p := range photos {
		pages[i] = grouping.Page{Image: p.data, MimeType: p.mime, Caption: p.caption}
	}
	idx, err := h.grouper.Group(ctx, pages, batchContext)
	if err != nil {
		h.log.Warn("grouping failed; filing each photo separately", "photos", len(photos), "err", err)
		return single()
	}
	out := make([][]item, len(idx))
	for i, g := range idx {
		for _, j := range g {
			out[i] = append(out[i], photos[j])
		}
	}
	return out
}

func (h *Handler) filePhotos(ctx context.Context, pages []item, batchContext string) {
	ctxText := contextFor(pages, batchContext)
	if len(pages) == 1 && ctxText == "" {
		// Keep a lone photo as-is.
		h.upload(ctx, pages, h.photoName(pages[0].mime), pages[0].data, "")
		return
	}
	images := make([]pdf.Image, len(pages))
	for i, p := range pages {
		images[i] = pdf.Image{Data: p.data, MimeType: p.mime}
	}
	data, err := pdf.FromImages(images, ctxText)
	if err != nil {
		h.fail(ctx, pages, fmt.Errorf("montar o PDF: %w", err))
		return
	}
	h.upload(ctx, pages, h.baseName()+".pdf", data, "")
}

// fileAsIs uploads a file unchanged. We can't add a context page to an
// arbitrary file, so the context becomes a note instead.
func (h *Handler) fileAsIs(ctx context.Context, f item, batchContext string) {
	name := f.name
	if name == "" {
		name = h.photoName(f.mime)
	}
	h.upload(ctx, []item{f}, name, f.data, contextFor([]item{f}, batchContext))
}

func (h *Handler) upload(ctx context.Context, msgs []item, name string, data []byte, note string) {
	taskID, err := h.paperless.Upload(ctx, name, data)
	if err != nil {
		h.fail(ctx, msgs, fmt.Errorf("enviar para o paperless: %w", err))
		return
	}
	waitCtx, cancel := context.WithTimeout(ctx, h.ConsumeTimeout)
	defer cancel()
	id, err := h.paperless.WaitForDocument(waitCtx, taskID)
	if err != nil {
		h.fail(ctx, msgs, fmt.Errorf("processar no paperless: %w", err))
		return
	}
	if note != "" {
		if err := h.paperless.AddNote(ctx, id, "Contexto enviado pelo WhatsApp: "+note); err != nil {
			h.log.Warn("could not add context note", "document", id, "err", err)
		}
	}
	h.log.Info("filed document", "document", id, "pages", len(msgs))
	h.react(ctx, msgs, reactDone)
}

func (h *Handler) fail(ctx context.Context, msgs []item, err error) {
	h.log.Error("filing failed", "err", err)
	h.react(ctx, msgs, reactFailed)
	if rerr := msgs[0].msg.Reply(ctx, fmt.Sprintf("⚠️ Não consegui arquivar: %v", err)); rerr != nil {
		h.log.Error("reply failed", "err", rerr)
	}
}

func (h *Handler) react(ctx context.Context, msgs []item, emoji string) {
	for _, it := range msgs {
		if err := it.msg.React(ctx, emoji); err != nil {
			h.log.Warn("react failed", "err", err)
		}
	}
}

// contextFor joins what the sender wrote for these pages: text messages sent
// during the batch and captions on the pages themselves.
func contextFor(pages []item, batchContext string) string {
	var parts []string
	if batchContext != "" {
		parts = append(parts, batchContext)
	}
	for _, p := range pages {
		if p.caption != "" {
			parts = append(parts, p.caption)
		}
	}
	return strings.Join(parts, "\n")
}

func (h *Handler) baseName() string {
	return "whatsapp-" + h.now().Format("20060102-150405")
}

func (h *Handler) photoName(mimeType string) string {
	ext := ".jpg"
	if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
		ext = exts[0]
		if ext == ".jpe" || ext == ".jfif" {
			ext = ".jpg"
		}
	}
	return h.baseName() + ext
}
