// Package documents files what's sent to the documents group in paperless.
//
// Photos sent close together are collected into a batch, grouped into
// documents by a vision LLM (so a multi-page contract becomes one PDF), and
// what the sender wrote about each document - a caption or a text message,
// like "carteira de vacinação do bebê" - is added to it so paperless' AI and
// search can use it. Photos sent a few minutes later can still join a filed
// document: it's replaced by one with all the pages. Progress is shown with
// reactions.
package documents

import (
	"context"
	"fmt"
	"log/slog"
	"mime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/grouping"
	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/pdf"
	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/router"
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
	Content(ctx context.Context, id int) (string, error)
	Delete(ctx context.Context, id int) error
}

type Grouper interface {
	Group(ctx context.Context, entries []grouping.Entry) ([]grouping.Document, error)
	SameDocument(ctx context.Context, r grouping.Recent, page grouping.Entry) (float64, error)
}

// mergeThreshold is how sure the LLM must be that a new photo continues a
// filed document before that document is replaced. A missed merge leaves
// two documents; a wrong one glues unrelated documents together.
const mergeThreshold = 0.9

// maxRecent is how many filed documents are offered for continuation.
const maxRecent = 3

// item is one message in a batch: a photo or file, already downloaded, or a
// text message (data nil).
type item struct {
	msg  router.Message
	data []byte
	mime string
	name string
	text string // the caption, or the text message itself
}

func (it item) isText() bool  { return it.data == nil }
func (it item) isPhoto() bool { return !it.isText() && pdf.Supported(it.mime) && it.name == "" }

type batch struct {
	items []item // in the order they were sent
	timer *time.Timer
}

type batchKey struct{ chat, sender string }

// filed is a photo document filed recently, kept so photos sent a few
// minutes later can be added to it.
type filed struct {
	id      int
	pages   []item
	context string
	text    string // paperless' OCR text
	at      time.Time
}

// sender is what's remembered between one sender's batches.
type sender struct {
	// mu files one batch at a time, so the next batch sees the documents
	// the previous one filed.
	mu     sync.Mutex
	texts  []item // text-only batches: probably about photos still to come
	textAt time.Time
	recent []*filed
}

// doc is a document about to be filed.
type doc struct {
	pages     []item
	context   string
	continues *filed // replaced by a document with its pages plus these
}

type Handler struct {
	paperless Paperless
	grouper   Grouper // nil: every photo becomes its own document
	log       *slog.Logger

	// BatchWindow is how long to wait after the last photo or text before
	// filing the batch. MergeWindow is how long a filed document stays open
	// to more pages, and TextWindow how long a text sent with no photo waits
	// for one. ConsumeTimeout bounds OCR + consumption per document.
	BatchWindow    time.Duration
	MergeWindow    time.Duration
	TextWindow     time.Duration
	ConsumeTimeout time.Duration
	now            func() time.Time

	mu      sync.Mutex
	batches map[batchKey]*batch
	senders map[batchKey]*sender
	wg      sync.WaitGroup // open batches, for tests
}

func New(p Paperless, g Grouper, log *slog.Logger) *Handler {
	return &Handler{
		paperless:   p,
		grouper:     g,
		log:         log,
		BatchWindow: 30 * time.Second,
		// People screenshot or photograph pages, then send them over the
		// next half hour or so.
		MergeWindow:    time.Hour,
		TextWindow:     10 * time.Minute,
		ConsumeTimeout: 10 * time.Minute,
		now:            time.Now,
		batches:        map[batchKey]*batch{},
		senders:        map[batchKey]*sender{},
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
		h.add(key, item{msg: m, text: text})
		return m.React(ctx, reactNoted)
	}

	// Download now: WhatsApp media links expire, and the batch may wait.
	data, err := m.Media.Download(ctx)
	if err != nil {
		return fmt.Errorf("baixar o arquivo do WhatsApp: %w", err)
	}
	h.add(key, item{msg: m, data: data, mime: m.Media.MimeType, name: m.Media.FileName, text: text})
	return m.React(ctx, reactWorking)
}

func (h *Handler) add(key batchKey, it item) {
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
	b.items = append(b.items, it)
}

// Wait blocks until all open batches have been filed. For tests.
func (h *Handler) Wait() { h.wg.Wait() }

func (h *Handler) flush(key batchKey) {
	defer h.wg.Done()
	h.mu.Lock()
	b := h.batches[key]
	delete(h.batches, key)
	s := h.senders[key]
	if s == nil {
		s = &sender{}
		h.senders[key] = s
	}
	h.mu.Unlock()
	if b == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := h.now()
	s.recent = slices.DeleteFunc(s.recent, func(f *filed) bool { return f.at.Before(now.Add(-h.MergeWindow)) })
	var entries []item
	if s.textAt.After(now.Add(-h.TextWindow)) {
		entries = s.texts
	}
	entries = append(slices.Clip(entries), b.items...)
	s.texts = nil
	if !slices.ContainsFunc(entries, func(it item) bool { return !it.isText() }) {
		s.texts, s.textAt = entries, now
		return
	}

	ctx := context.Background()
	positional := positionalContext(entries)
	docs := h.group(ctx, entries, positional)
	h.findContinued(ctx, s.recent, docs)
	for _, d := range docs {
		h.filePhotos(ctx, s, d)
	}
	for i, it := range entries {
		if !it.isText() && !it.isPhoto() {
			h.fileAsIs(ctx, it, positional[i])
		}
	}
}

// group splits photos into documents. Without a grouper, or when it fails,
// each photo is its own document and texts are assigned by position.
func (h *Handler) group(ctx context.Context, entries []item, positional []string) []doc {
	var single []doc
	for i, it := range entries {
		if it.isPhoto() {
			single = append(single, doc{pages: []item{it}, context: positional[i]})
		}
	}
	if h.grouper == nil || len(single) < 2 {
		return single
	}

	var photos []item
	var gEntries []grouping.Entry
	for _, it := range entries {
		switch {
		case it.isText():
			gEntries = append(gEntries, grouping.Entry{Text: it.text})
		case it.isPhoto():
			photos = append(photos, it)
			gEntries = append(gEntries, grouping.Entry{Image: it.data, MimeType: it.mime, Text: it.text})
		}
	}
	docs, err := h.grouper.Group(ctx, gEntries)
	if err != nil {
		h.log.Warn("grouping failed; filing each photo separately", "photos", len(photos), "err", err)
		return single
	}

	out := make([]doc, len(docs))
	for i, d := range docs {
		var captions []string
		for _, j := range d.Pages {
			out[i].pages = append(out[i].pages, photos[j])
			if photos[j].text != "" {
				captions = append(captions, photos[j].text)
			}
		}
		out[i].context = d.Context
		if out[i].context == "" {
			out[i].context = strings.Join(captions, "\n")
		}
	}
	return out
}

// findContinued checks each new document against the documents filed in the
// last few minutes, and marks it as their continuation only if the LLM is
// sure its first page belongs to one of them.
func (h *Handler) findContinued(ctx context.Context, recent []*filed, docs []doc) {
	if h.grouper == nil {
		return
	}
	taken := map[*filed]bool{}
	for i := range docs {
		page := docs[i].pages[0]
		best, bestP := (*filed)(nil), mergeThreshold
		for _, f := range recent {
			if taken[f] {
				continue
			}
			p, err := h.grouper.SameDocument(ctx, toRecent(f), grouping.Entry{Image: page.data, MimeType: page.mime})
			if err != nil {
				h.log.Warn("could not compare with a recent document", "document", f.id, "err", err)
				continue
			}
			h.log.Info("compared with a recent document", "document", f.id, "p", p)
			if p >= bestP {
				best, bestP = f, p
			}
		}
		if best != nil {
			docs[i].continues, taken[best] = best, true
		}
	}
}

func (h *Handler) filePhotos(ctx context.Context, s *sender, d doc) {
	pages, ctxText := d.pages, d.context
	if f := d.continues; f != nil {
		pages = append(slices.Clip(f.pages), d.pages...)
		ctxText = joinContexts(f.context, d.context)
	}

	var name string
	var data []byte
	if len(pages) == 1 && ctxText == "" {
		// Keep a lone photo as-is.
		name, data = h.photoName(pages[0].mime), pages[0].data
	} else {
		images := make([]pdf.Image, len(pages))
		for i, p := range pages {
			images[i] = pdf.Image{Data: p.data, MimeType: p.mime}
		}
		var err error
		if data, err = pdf.FromImages(images, ctxText); err != nil {
			h.fail(ctx, d.pages, fmt.Errorf("montar o PDF: %w", err))
			return
		}
		name = h.baseName() + ".pdf"
	}
	id, ok := h.upload(ctx, d.pages, name, data, "")
	if !ok {
		return
	}

	if f := d.continues; f != nil {
		// Only now that the combined document exists: a failure here leaves
		// a duplicate, never a lost page.
		if err := h.paperless.Delete(ctx, f.id); err != nil {
			h.log.Warn("merged, but could not delete the old document", "old", f.id, "new", id, "err", err)
		}
		s.recent = slices.DeleteFunc(s.recent, func(r *filed) bool { return r == f })
		h.log.Info("added pages to a recent document", "old", f.id, "new", id, "pages", len(pages))
	}
	text, err := h.paperless.Content(ctx, id)
	if err != nil {
		h.log.Warn("could not read document text", "document", id, "err", err)
	}
	s.recent = append(s.recent, &filed{id: id, pages: pages, context: ctxText, text: text, at: h.now()})
	if len(s.recent) > maxRecent {
		s.recent = s.recent[len(s.recent)-maxRecent:]
	}
}

// fileAsIs uploads a file unchanged. We can't add a context page to an
// arbitrary file, so the context becomes a note instead.
func (h *Handler) fileAsIs(ctx context.Context, f item, context string) {
	name := f.name
	if name == "" {
		name = h.photoName(f.mime)
	}
	h.upload(ctx, []item{f}, name, f.data, context)
}

// upload files data in paperless and reacts on msgs, returning the new
// document's ID and whether it worked.
func (h *Handler) upload(ctx context.Context, msgs []item, name string, data []byte, note string) (int, bool) {
	taskID, err := h.paperless.Upload(ctx, name, data)
	if err != nil {
		h.fail(ctx, msgs, fmt.Errorf("enviar para o paperless: %w", err))
		return 0, false
	}
	waitCtx, cancel := context.WithTimeout(ctx, h.ConsumeTimeout)
	defer cancel()
	id, err := h.paperless.WaitForDocument(waitCtx, taskID)
	if err != nil {
		h.fail(ctx, msgs, fmt.Errorf("processar no paperless: %w", err))
		return 0, false
	}
	if note != "" {
		if err := h.paperless.AddNote(ctx, id, "Contexto enviado pelo WhatsApp: "+note); err != nil {
			h.log.Warn("could not add context note", "document", id, "err", err)
		}
	}
	h.log.Info("filed document", "document", id, "pages", len(msgs))
	h.react(ctx, msgs, reactDone)
	return id, true
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

// positionalContext is what the sender wrote for each photo or file, judged
// by position: a text message applies to the photos and files sent after
// it, up to the next text; a text with nothing after it applies to those
// just before it. A caption always stays with its own photo or file.
func positionalContext(entries []item) []string {
	texts := make([][]string, len(entries))
	var pending []string // texts not yet given to any photo or file
	var run, prev []int  // photos and files since the last text; the run before
	assign := func() {
		for _, i := range run {
			texts[i] = append(texts[i], pending...)
		}
	}
	for i, it := range entries {
		if !it.isText() {
			run = append(run, i)
			continue
		}
		if len(run) > 0 {
			assign()
			pending, prev, run = nil, run, nil
		}
		pending = append(pending, it.text)
	}
	if len(run) == 0 {
		run = prev
	}
	assign()

	out := make([]string, len(entries))
	for i, it := range entries {
		if it.isText() {
			continue
		}
		if it.text != "" {
			texts[i] = append(texts[i], it.text)
		}
		out[i] = strings.Join(texts[i], "\n")
	}
	return out
}

// toRecent describes a filed document for the LLM: by an excerpt of its
// text, or by its last page when OCR found little text (the back of an ID
// card, a signature page).
func toRecent(f *filed) grouping.Recent {
	var r grouping.Recent
	text := []rune(strings.TrimSpace(f.text))
	if len(text) < 100 {
		last := f.pages[len(f.pages)-1]
		r.Image, r.MimeType = last.data, last.mime
		return r
	}
	if len(text) > 1500 {
		text = append(append(text[:1000:1000], []rune("\n[...]\n")...), text[len(text)-500:]...)
	}
	r.Text = string(text)
	return r
}

// joinContexts joins the context of a filed document and of pages added to
// it, without repeating it when the sender said the same thing twice.
func joinContexts(old, added string) string {
	switch {
	case old == "" || old == added:
		return added
	case added == "":
		return old
	}
	return old + "\n" + added
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
