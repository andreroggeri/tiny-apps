// Package documents files photos and PDFs sent to the documents group in
// paperless, then replies with what paperless (and its AI workflow) made of it.
package documents

import (
	"context"
	"fmt"
	"mime"
	"path/filepath"
	"strings"
	"time"

	"github.com/andreroggeri/pombo/internal/paperless"
	"github.com/andreroggeri/pombo/internal/router"
)

// Name is the handler name shown in the dashboard.
const Name = "documents"

type Paperless interface {
	Upload(ctx context.Context, filename string, data []byte) (string, error)
	WaitForDocument(ctx context.Context, taskID string) (int, error)
	WaitForTitleChange(ctx context.Context, id int, initial string) (paperless.Document, error)
}

type Handler struct {
	paperless Paperless
	publicURL string // paperless URL for links in replies

	// ConsumeTimeout bounds OCR + consumption; AITimeout bounds the wait for
	// the AI workflow to rename the document. Exported for tests.
	ConsumeTimeout time.Duration
	AITimeout      time.Duration
	now            func() time.Time
}

func New(p Paperless, publicURL string) *Handler {
	return &Handler{
		paperless:      p,
		publicURL:      strings.TrimRight(publicURL, "/"),
		ConsumeTimeout: 10 * time.Minute,
		AITimeout:      3 * time.Minute,
		now:            time.Now,
	}
}

func (h *Handler) Handle(ctx context.Context, m router.Message) error {
	if m.Media == nil {
		return nil // text messages: nothing to file (questions come later)
	}
	data, err := m.Media.Download(ctx)
	if err != nil {
		return fmt.Errorf("baixar o arquivo do WhatsApp: %w", err)
	}

	name := h.fileName(m.Media)
	if err := m.Reply(ctx, "📥 Recebido, arquivando…"); err != nil {
		return err
	}

	taskID, err := h.paperless.Upload(ctx, name, data)
	if err != nil {
		return fmt.Errorf("enviar para o paperless: %w", err)
	}

	consumeCtx, cancel := context.WithTimeout(ctx, h.ConsumeTimeout)
	defer cancel()
	docID, err := h.paperless.WaitForDocument(consumeCtx, taskID)
	if err != nil {
		return fmt.Errorf("processar no paperless: %w", err)
	}

	// Paperless titles a new document after its file name; the AI workflow
	// replaces that shortly after. Wait for it, but reply regardless.
	aiCtx, cancelAI := context.WithTimeout(ctx, h.AITimeout)
	defer cancelAI()
	doc, err := h.paperless.WaitForTitleChange(aiCtx, docID, strings.TrimSuffix(name, filepath.Ext(name)))
	if err != nil {
		return fmt.Errorf("ler o documento %d: %w", docID, err)
	}
	return m.Reply(ctx, h.summary(doc))
}

// fileName keeps a document's own name; photos have none, so they get a
// timestamped one.
func (h *Handler) fileName(media *router.Media) string {
	if media.FileName != "" {
		return media.FileName
	}
	ext := ".jpg"
	if exts, _ := mime.ExtensionsByType(media.MimeType); len(exts) > 0 {
		ext = exts[0]
		if ext == ".jpe" || ext == ".jfif" {
			ext = ".jpg"
		}
	}
	return "whatsapp-" + h.now().Format("20060102-150405") + ext
}

func (h *Handler) summary(doc paperless.Document) string {
	var b strings.Builder
	fmt.Fprintf(&b, "✅ Arquivado: *%s*", doc.Title)
	if doc.Correspondent != "" {
		fmt.Fprintf(&b, "\n👤 %s", doc.Correspondent)
	}
	if len(doc.Tags) > 0 {
		fmt.Fprintf(&b, "\n🏷️ %s", strings.Join(doc.Tags, ", "))
	}
	if h.publicURL != "" {
		fmt.Fprintf(&b, "\n🔗 %s/documents/%d/details", h.publicURL, doc.ID)
	}
	return b.String()
}
