// Package dash serves the one-page management dashboard: WhatsApp status (or
// the pairing QR code), which group goes to which handler, and who may use
// the bot. It has no auth of its own; put it behind the ingress SSO.
package dash

import (
	"context"
	"embed"
	"encoding/base64"
	"html/template"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/settings"
	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/whatsapp"
)

//go:embed page.html
var pageFS embed.FS

var page = template.Must(template.ParseFS(pageFS, "page.html"))

type WhatsApp interface {
	Status() whatsapp.Status
	Groups(ctx context.Context) ([]whatsapp.Group, error)
}

type Settings interface {
	Routes(ctx context.Context) ([]settings.Route, error)
	SetRoute(ctx context.Context, r settings.Route) error
	Senders(ctx context.Context) ([]settings.Sender, error)
	AddSender(ctx context.Context, s settings.Sender) error
	RemoveSender(ctx context.Context, phone string) error
}

type Server struct {
	wa       WhatsApp
	settings Settings
	handlers []string
	log      *slog.Logger
}

func New(wa WhatsApp, s Settings, handlerNames []string, log *slog.Logger) *Server {
	return &Server{wa: wa, settings: s, handlers: handlerNames, log: log}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("POST /routes", s.setRoute)
	mux.HandleFunc("POST /senders", s.addSender)
	mux.HandleFunc("POST /senders/delete", s.removeSender)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	return mux
}

type groupRow struct {
	JID, Name, Handler string
	Joined             bool // false: routed earlier, but the bot left the group
}

type pageData struct {
	Status   whatsapp.Status
	QRImage  template.URL
	Groups   []groupRow
	Handlers []string
	Senders  []settings.Sender
	Error    string
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := pageData{Status: s.wa.Status(), Handlers: s.handlers}

	if data.Status.QRCode != "" {
		png, err := qrcode.Encode(data.Status.QRCode, qrcode.Medium, 320)
		if err == nil {
			data.QRImage = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
		}
	}

	rows, err := s.groupRows(ctx)
	if err != nil {
		data.Error = "Não consegui listar os grupos: " + err.Error()
	}
	data.Groups = rows

	if data.Senders, err = s.settings.Senders(ctx); err != nil {
		data.Error = err.Error()
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.Execute(w, data); err != nil {
		s.log.Error("render dashboard", "err", err)
	}
}

// groupRows merges the groups the bot is in with the stored routes.
func (s *Server) groupRows(ctx context.Context) ([]groupRow, error) {
	routes, err := s.settings.Routes(ctx)
	if err != nil {
		return nil, err
	}
	byJID := map[string]*groupRow{}
	for _, r := range routes {
		byJID[r.GroupJID] = &groupRow{JID: r.GroupJID, Name: r.GroupName, Handler: r.Handler}
	}
	groups, err := s.wa.Groups(ctx)
	for _, g := range groups {
		row, ok := byJID[g.JID]
		if !ok {
			row = &groupRow{JID: g.JID}
			byJID[g.JID] = row
		}
		row.Name, row.Joined = g.Name, true
	}

	out := make([]groupRow, 0, len(byJID))
	for _, row := range byJID {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, err
}

func (s *Server) setRoute(w http.ResponseWriter, r *http.Request) {
	handler := r.FormValue("handler")
	if handler != "" && !s.knownHandler(handler) {
		http.Error(w, "unknown handler", http.StatusBadRequest)
		return
	}
	route := settings.Route{GroupJID: r.FormValue("group_jid"), GroupName: r.FormValue("group_name"), Handler: handler}
	if route.GroupJID == "" {
		http.Error(w, "missing group", http.StatusBadRequest)
		return
	}
	s.saveAndRedirect(w, r, s.settings.SetRoute(r.Context(), route))
}

func (s *Server) addSender(w http.ResponseWriter, r *http.Request) {
	phone := normalizePhone(r.FormValue("phone"))
	if phone == "" {
		http.Error(w, "missing phone", http.StatusBadRequest)
		return
	}
	snd := settings.Sender{Phone: phone, Name: strings.TrimSpace(r.FormValue("name"))}
	s.saveAndRedirect(w, r, s.settings.AddSender(r.Context(), snd))
}

func (s *Server) removeSender(w http.ResponseWriter, r *http.Request) {
	s.saveAndRedirect(w, r, s.settings.RemoveSender(r.Context(), r.FormValue("phone")))
}

func (s *Server) saveAndRedirect(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		s.log.Error("save settings", "path", r.URL.Path, "err", err)
		http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) knownHandler(name string) bool {
	for _, h := range s.handlers {
		if h == name {
			return true
		}
	}
	return false
}

// normalizePhone keeps digits only: "+55 (11) 99999-8888" → "5511999998888".
func normalizePhone(in string) string {
	var b strings.Builder
	for _, r := range in {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
