// Package notify posts a message in the documents group when coruja reports
// that a new document was filed and summarized.
package notify

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/settings"
)

type WhatsApp interface {
	Send(ctx context.Context, chatJID, text string) error
}

type Routes interface {
	Routes(ctx context.Context) ([]settings.Route, error)
}

// Document is coruja's report about a new document.
type Document struct {
	DocumentID int    `json:"document_id"`
	Title      string `json:"title"`
	Summary    string `json:"summary"`
	SentBy     string `json:"sent_by"`
	URL        string `json:"url"`
}

type Handler struct {
	wa      WhatsApp
	routes  Routes
	token   string
	handler string // groups routed to this handler get the message
	log     *slog.Logger
}

// New returns the POST /notify endpoint. Callers authenticate with token as
// a bearer token; an empty token turns the endpoint off.
func New(wa WhatsApp, routes Routes, token, handler string, log *slog.Logger) *Handler {
	return &Handler{wa: wa, routes: routes, token: token, handler: handler, log: log}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.token == "" || r.Header.Get("Authorization") != "Bearer "+h.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var doc Document
	if err := json.NewDecoder(r.Body).Decode(&doc); err != nil || doc.Title == "" {
		http.Error(w, "bad document", http.StatusBadRequest)
		return
	}
	routes, err := h.routes.Routes(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	text, sent := Text(doc), 0
	for _, route := range routes {
		if route.Handler != h.handler {
			continue
		}
		if err := h.wa.Send(r.Context(), route.GroupJID, text); err != nil {
			h.log.Error("notification failed", "group", route.GroupJID, "document", doc.DocumentID, "err", err)
			continue
		}
		sent++
	}
	if sent == 0 {
		http.Error(w, "no group got the message", http.StatusBadGateway)
		return
	}
	h.log.Info("notified", "document", doc.DocumentID, "groups", sent)
	w.WriteHeader(http.StatusNoContent)
}

// Text is the chat message for a document, e.g.:
//
//	📄 *CNH do André*
//	Carteira de motorista, categoria AB, válida até 2031.
//	Enviado por Luana · https://docs.example/documents/8/details
func Text(doc Document) string {
	lines := []string{"📄 *" + doc.Title + "*"}
	if doc.Summary != "" {
		lines = append(lines, doc.Summary)
	}
	var last []string
	if doc.SentBy != "" {
		last = append(last, "Enviado por "+doc.SentBy)
	}
	if doc.URL != "" {
		last = append(last, doc.URL)
	}
	if len(last) > 0 {
		lines = append(lines, strings.Join(last, " · "))
	}
	return strings.Join(lines, "\n")
}
