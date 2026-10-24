// Package router decides what happens to an incoming group message: it looks
// up which handler the group is routed to and drops anything from people who
// aren't on the allowlist.
package router

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
)

// Message is a group message, independent of the WhatsApp library.
type Message struct {
	ChatJID     string
	SenderPhone string // international format without "+"; "" if unknown
	Text        string // body or media caption
	Media       *Media // nil for text-only messages

	ReplyFunc func(ctx context.Context, text string) error
	ReactFunc func(ctx context.Context, emoji string) error
}

func (m Message) Reply(ctx context.Context, text string) error {
	if m.ReplyFunc == nil {
		return nil
	}
	return m.ReplyFunc(ctx, text)
}

// React puts an emoji reaction on the message; a later reaction replaces it.
func (m Message) React(ctx context.Context, emoji string) error {
	if m.ReactFunc == nil {
		return nil
	}
	return m.ReactFunc(ctx, emoji)
}

// Media is an attachment. Download is lazy so ignored messages cost nothing.
type Media struct {
	FileName string // "" for photos, which carry no name
	MimeType string
	Download func(ctx context.Context) ([]byte, error)
}

type Handler interface {
	Handle(ctx context.Context, m Message) error
}

// Settings is the part of the settings store the router needs.
type Settings interface {
	HandlerFor(ctx context.Context, groupJID string) (string, error)
	IsAllowed(ctx context.Context, phone string) (bool, error)
}

type Router struct {
	settings Settings
	handlers map[string]Handler
	log      *slog.Logger
}

func New(settings Settings, handlers map[string]Handler, log *slog.Logger) *Router {
	return &Router{settings: settings, handlers: handlers, log: log}
}

// HandlerNames lists the handlers a group can be routed to, for the dashboard.
func (r *Router) HandlerNames() []string {
	names := make([]string, 0, len(r.handlers))
	for n := range r.handlers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Dispatch runs the routed handler for m, if any. Errors are logged and
// reported back in the chat, so the sender knows something went wrong.
func (r *Router) Dispatch(ctx context.Context, m Message) {
	name, err := r.settings.HandlerFor(ctx, m.ChatJID)
	if err != nil {
		r.log.Error("route lookup failed", "chat", m.ChatJID, "err", err)
		return
	}
	if name == "" {
		return // group not routed
	}
	h, ok := r.handlers[name]
	if !ok {
		r.log.Warn("group routed to unknown handler", "chat", m.ChatJID, "handler", name)
		return
	}

	allowed, err := r.settings.IsAllowed(ctx, m.SenderPhone)
	if err != nil {
		r.log.Error("allowlist lookup failed", "err", err)
		return
	}
	if !allowed {
		r.log.Info("ignoring message from sender not on allowlist", "chat", m.ChatJID, "sender", m.SenderPhone)
		return
	}

	if err := h.Handle(ctx, m); err != nil {
		r.log.Error("handler failed", "handler", name, "chat", m.ChatJID, "err", err)
		_ = m.React(ctx, "❌")
		if rerr := m.Reply(ctx, fmt.Sprintf("⚠️ Não consegui processar: %v", err)); rerr != nil {
			r.log.Error("reply failed", "err", rerr)
		}
	}
}
