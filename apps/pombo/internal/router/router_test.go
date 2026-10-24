package router

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

type fakeSettings struct {
	routes  map[string]string
	allowed map[string]bool
}

func (f fakeSettings) HandlerFor(_ context.Context, jid string) (string, error) {
	return f.routes[jid], nil
}

func (f fakeSettings) AllowedSender(_ context.Context, phone string) (string, bool, error) {
	if !f.allowed[phone] {
		return "", false, nil
	}
	return "Luana", true, nil
}

type recordingHandler struct {
	got []Message
	err error
}

func (h *recordingHandler) Handle(_ context.Context, m Message) error {
	h.got = append(h.got, m)
	return h.err
}

func newRouter(h Handler) *Router {
	s := fakeSettings{
		routes:  map[string]string{"docs@g.us": "documents", "ghost@g.us": "removed"},
		allowed: map[string]bool{"5511999998888": true},
	}
	return New(s, map[string]Handler{"documents": h}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestDispatch(t *testing.T) {
	tests := []struct {
		name   string
		msg    Message
		called bool
	}{
		{"routed group, allowed sender", Message{ChatJID: "docs@g.us", SenderPhone: "5511999998888"}, true},
		{"routed group, unknown sender", Message{ChatJID: "docs@g.us", SenderPhone: "5511000000000"}, false},
		{"routed group, unresolved sender", Message{ChatJID: "docs@g.us", SenderPhone: ""}, false},
		{"group not routed", Message{ChatJID: "family@g.us", SenderPhone: "5511999998888"}, false},
		{"group routed to missing handler", Message{ChatJID: "ghost@g.us", SenderPhone: "5511999998888"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &recordingHandler{}
			newRouter(h).Dispatch(context.Background(), tt.msg)
			if got := len(h.got) == 1; got != tt.called {
				t.Fatalf("handler called = %v, want %v", got, tt.called)
			}
			if tt.called && h.got[0].SenderName != "Luana" {
				t.Errorf("SenderName = %q, want the allowlist name", h.got[0].SenderName)
			}
		})
	}
}

func TestDispatchRepliesWithHandlerError(t *testing.T) {
	h := &recordingHandler{err: errors.New("paperless is down")}
	var reply string
	msg := Message{
		ChatJID:     "docs@g.us",
		SenderPhone: "5511999998888",
		ReplyFunc:   func(_ context.Context, text string) error { reply = text; return nil },
	}
	newRouter(h).Dispatch(context.Background(), msg)
	if !strings.Contains(reply, "paperless is down") {
		t.Fatalf("reply = %q, want it to mention the error", reply)
	}
}

func TestHandlerNamesSorted(t *testing.T) {
	r := New(fakeSettings{}, map[string]Handler{"home": nil, "documents": nil}, slog.Default())
	if got := strings.Join(r.HandlerNames(), ","); got != "documents,home" {
		t.Fatalf("HandlerNames = %s", got)
	}
}
