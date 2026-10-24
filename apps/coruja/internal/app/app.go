// Package app is coruja's job: when paperless reports a new document, wait
// for paperless' own AI to finish with it, write a title and a one-line
// summary, share whatever paperless created for it, and tell pombo.
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/andreroggeri/tiny-apps/apps/coruja/internal/llm"
	"github.com/andreroggeri/tiny-apps/apps/coruja/internal/paperless"
	"github.com/andreroggeri/tiny-apps/apps/coruja/internal/pdfimage"
)

type Paperless interface {
	Document(ctx context.Context, id int) (paperless.Document, error)
	Original(ctx context.Context, id int) ([]byte, error)
	WaitForAI(ctx context.Context, id int, grace time.Duration) error
	Update(ctx context.Context, id int, patch map[string]any) error
	Share(ctx context.Context, kind string, id int) (bool, error)
}

type Summarizer interface {
	Summarize(ctx context.Context, doc llm.Document) (llm.Summary, error)
}

type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// Notification is what pombo receives for each new document.
type Notification struct {
	DocumentID int    `json:"document_id"`
	Title      string `json:"title"`
	Summary    string `json:"summary"` // "" when the LLM failed
	SentBy     string `json:"sent_by"` // "" when not sent through pombo
	URL        string `json:"url"`
}

type App struct {
	paperless Paperless
	llm       Summarizer
	notifier  Notifier // nil: no notifications
	log       *slog.Logger

	// Token is the shared secret paperless' webhook sends as a bearer token.
	Token string
	// SummaryField and SentByField are custom field IDs; SentByField 0
	// means unknown.
	SummaryField int
	SentByField  int
	// PublicURL is paperless as people open it, for links.
	PublicURL string
	// AIGrace is how long to wait for paperless' AI task to show up at all;
	// AITimeout bounds waiting for it to finish (it takes minutes).
	AIGrace   time.Duration
	AITimeout time.Duration

	queue chan int
}

func New(p Paperless, s Summarizer, n Notifier, log *slog.Logger) *App {
	return &App{
		paperless: p,
		llm:       s,
		notifier:  n,
		log:       log,
		AIGrace:   30 * time.Second,
		AITimeout: 15 * time.Minute,
		queue:     make(chan int, 100),
	}
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook", a.webhook)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	return mux
}

// webhook takes paperless' "document added" call, {"doc_id": "12"}, and
// queues the document. Work happens in Run, one document at a time: Gemma
// serves one request at a time anyway.
func (a *App) webhook(w http.ResponseWriter, r *http.Request) {
	if a.Token == "" || r.Header.Get("Authorization") != "Bearer "+a.Token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body struct {
		DocID json.RawMessage `json:"doc_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad JSON", http.StatusBadRequest)
		return
	}
	id, err := strconv.Atoi(strings.Trim(string(body.DocID), `"`))
	if err != nil || id <= 0 {
		http.Error(w, "bad doc_id", http.StatusBadRequest)
		return
	}
	select {
	case a.queue <- id:
		a.log.Info("queued document", "document", id)
		w.WriteHeader(http.StatusAccepted)
	default:
		http.Error(w, "queue full", http.StatusServiceUnavailable)
	}
}

// Run processes queued documents until ctx is done.
func (a *App) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-a.queue:
			docCtx, cancel := context.WithTimeout(ctx, a.AITimeout+5*time.Minute)
			if err := a.Process(docCtx, id); err != nil {
				a.log.Error("processing failed", "document", id, "err", err)
			}
			cancel()
		}
	}
}

// Process handles one document. A document that already has a summary is
// left alone, so a repeated webhook does nothing.
func (a *App) Process(ctx context.Context, id int) error {
	waitCtx, cancel := context.WithTimeout(ctx, a.AITimeout)
	if err := a.paperless.WaitForAI(waitCtx, id, a.AIGrace); err != nil {
		a.log.Warn("gave up waiting for paperless' AI; going ahead", "document", id, "err", err)
	}
	cancel()

	doc, err := a.paperless.Document(ctx, id)
	if err != nil {
		return err
	}
	if doc.Field(a.SummaryField) != "" {
		a.log.Info("already summarized", "document", id)
		return nil
	}

	in := llm.Document{Text: doc.Content}
	switch doc.MimeType {
	case "image/jpeg", "image/png":
		if in.Image, err = a.paperless.Original(ctx, id); err != nil {
			a.log.Warn("could not download the photo; using its text only", "document", id, "err", err)
		}
		in.MimeType = doc.MimeType
	case "application/pdf":
		// A PDF of photos (pombo's) has garbled OCR text, enough to get a
		// date wrong; its first page as an image fixes that. A PDF with
		// real text has no photo and goes by its text.
		data, err := a.paperless.Original(ctx, id)
		if err != nil {
			a.log.Warn("could not download the PDF; using its text only", "document", id, "err", err)
		} else if in.Image = pdfimage.FirstJPEG(data, 20<<10); in.Image != nil {
			in.MimeType = "image/jpeg"
		}
	}
	title := doc.Title
	s, err := a.llm.Summarize(ctx, in)
	if err != nil {
		s = llm.Summary{}
		a.log.Error("summary failed; notifying without one", "document", id, "err", err)
	} else {
		patch := map[string]any{"custom_fields": withField(doc.CustomFields, a.SummaryField, s.Summary)}
		if untitled(doc) {
			patch["title"], title = s.Title, s.Title
		}
		if err := a.paperless.Update(ctx, id, patch); err != nil {
			return err
		}
		a.log.Info("summarized", "document", id, "title", title)
	}

	a.share(ctx, doc)
	if a.notifier == nil {
		return nil
	}
	return a.notifier.Notify(ctx, Notification{
		DocumentID: id,
		Title:      title,
		Summary:    s.Summary,
		SentBy:     doc.Field(a.SentByField),
		URL:        strings.TrimRight(a.PublicURL, "/") + "/documents/" + strconv.Itoa(id) + "/details",
	})
}

// share makes the document's tags, correspondent and type visible to every
// user. paperless' AI creates them owned by whoever uploaded the document
// (pombo, for WhatsApp), which hides them from everyone else.
func (a *App) share(ctx context.Context, doc paperless.Document) {
	objs := map[string][]int{"tags": doc.Tags}
	if doc.Correspondent != nil {
		objs["correspondents"] = []int{*doc.Correspondent}
	}
	if doc.DocumentType != nil {
		objs["document_types"] = []int{*doc.DocumentType}
	}
	for kind, ids := range objs {
		for _, id := range ids {
			changed, err := a.paperless.Share(ctx, kind, id)
			if err != nil {
				a.log.Warn("could not share", "kind", kind, "id", id, "err", err)
			} else if changed {
				a.log.Info("shared with everyone", "kind", kind, "id", id)
			}
		}
	}
}

// untitled reports whether the title is still paperless' default, the file
// name, so a title someone (or paperless' AI) chose is never overwritten.
func untitled(doc paperless.Document) bool {
	name := doc.OriginalFileName
	return doc.Title == strings.TrimSuffix(name, path.Ext(name)) || doc.Title == name
}

// withField returns fields with field set to value. paperless replaces the
// whole list on update, so the others must be sent back as they were.
func withField(fields []paperless.FieldValue, field int, value string) []paperless.FieldValue {
	out := make([]paperless.FieldValue, 0, len(fields)+1)
	for _, f := range fields {
		if f.Field != field {
			out = append(out, f)
		}
	}
	return append(out, paperless.FieldValue{Field: field, Value: value})
}

// HTTPNotifier posts notifications as JSON to pombo.
type HTTPNotifier struct {
	URL   string
	Token string
	HTTP  *http.Client
}

func (n *HTTPNotifier) Notify(ctx context.Context, msg Notification) error {
	body, _ := json.Marshal(msg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+n.Token)
	resp, err := n.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("notify: %s", resp.Status)
	}
	return nil
}
