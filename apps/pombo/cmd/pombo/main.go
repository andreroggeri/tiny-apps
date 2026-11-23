// pombo is a WhatsApp bot that sits in family groups and routes each
// group's messages to a handler (e.g. filing documents in paperless).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/dash"
	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/grouping"
	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/handlers/documents"
	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/handlers/reservas"
	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/notify"
	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/paperless"
	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/router"
	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/settings"
	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/superlogica"
	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/whatsapp"

	_ "time/tzdata" // the image has no zoneinfo; reservas needs America/Sao_Paulo
)

// sentByField is the paperless custom field recording who sent a document,
// created by hand once.
const sentByField = "Enviado por"

type config struct {
	DataDir        string
	Listen         string
	PaperlessURL   string // API base, reachable from the bot
	PaperlessToken string
	LLMURL         string // OpenAI-compatible API with image input; "" disables grouping
	LLMModel       string
	NotifyToken    string // bearer token coruja sends to /notify; "" disables it

	// Condo bookings (reservas handler).
	SuperlogicaURL      string
	SuperlogicaCondoID  string
	SuperlogicaUnitID   string
	SuperlogicaLoginURL string // external login service; "" = renew in-process through the SSO session
}

func loadConfig() (config, error) {
	c := config{
		DataDir:        getenv("POMBO_DATA_DIR", "./data"),
		Listen:         getenv("POMBO_LISTEN", ":8080"),
		PaperlessURL:   os.Getenv("PAPERLESS_URL"),
		PaperlessToken: os.Getenv("PAPERLESS_TOKEN"),
		LLMURL:         os.Getenv("LLM_URL"),
		LLMModel:       os.Getenv("LLM_MODEL"),
		NotifyToken:    os.Getenv("NOTIFY_TOKEN"),

		SuperlogicaURL:      os.Getenv("SUPERLOGICA_URL"),
		SuperlogicaCondoID:  os.Getenv("SUPERLOGICA_CONDO_ID"),
		SuperlogicaUnitID:   os.Getenv("SUPERLOGICA_UNIT_ID"),
		SuperlogicaLoginURL: os.Getenv("SUPERLOGICA_LOGIN_URL"),
	}
	if c.PaperlessURL == "" || c.PaperlessToken == "" {
		return c, errors.New("PAPERLESS_URL and PAPERLESS_TOKEN are required")
	}
	if c.LLMURL != "" && c.LLMModel == "" {
		return c, errors.New("LLM_MODEL is required when LLM_URL is set")
	}
	// Deliberately not defaulted: which condo and unit these are is nobody's
	// business but the deployment's, so they stay out of the repo.
	if c.SuperlogicaURL == "" || c.SuperlogicaCondoID == "" || c.SuperlogicaUnitID == "" {
		return c, errors.New("SUPERLOGICA_URL, SUPERLOGICA_CONDO_ID and SUPERLOGICA_UNIT_ID are required")
	}
	return c, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := settings.Open(ctx, filepath.Join(cfg.DataDir, "pombo.db"))
	if err != nil {
		return fmt.Errorf("open settings: %w", err)
	}
	defer store.Close()

	pl := paperless.New(cfg.PaperlessURL, cfg.PaperlessToken)
	var grouper documents.Grouper
	if cfg.LLMURL != "" {
		grouper = grouping.NewLLM(cfg.LLMURL, cfg.LLMModel)
	} else {
		log.Warn("LLM_URL not set: photos sent together are filed as separate documents")
	}
	docs := documents.New(pl, grouper, log)
	if docs.SentByField, err = pl.CustomFieldID(ctx, sentByField); err != nil {
		log.Warn("not recording who sent documents", "err", err)
	}
	condo, err := superlogica.New(cfg.SuperlogicaURL, cfg.SuperlogicaCondoID, cfg.SuperlogicaUnitID,
		filepath.Join(cfg.DataDir, "superlogica-session.json"))
	if err != nil {
		return err
	}
	// Renewing needs no browser and no separate service: the saved session
	// carries the Keycloak cookies, which mint a new app session on their
	// own. SUPERLOGICA_LOGIN_URL is left as an escape hatch for the day the
	// login form has to be driven after all.
	var login superlogica.Login = superlogica.NewSSOLogin(condo)
	if cfg.SuperlogicaLoginURL != "" {
		login = superlogica.NewHTTPLogin(cfg.SuperlogicaLoginURL)
		log.Info("renewing condo sessions through an external login service", "url", cfg.SuperlogicaLoginURL)
	}
	if !condo.HasSession() {
		log.Warn("no condo session saved yet: paste one in the dashboard, cookies for both superlogica hosts")
	}
	site := superlogica.NewSite(condo, login, log)
	saoPaulo, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		return err
	}
	pending, err := reservas.OpenStore(ctx, filepath.Join(cfg.DataDir, "reservas.db"), saoPaulo)
	if err != nil {
		return fmt.Errorf("open reservas store: %w", err)
	}
	defer pending.Close()
	sender := &lateSender{}
	handlers := map[string]router.Handler{documents.Name: docs}
	var booker *reservas.Handler
	if cfg.LLMURL != "" {
		booker = reservas.New(site, pending, sender, reservas.NewLLM(cfg.LLMURL, cfg.LLMModel), saoPaulo, log)
		handlers[reservas.Name] = booker
	} else {
		log.Warn("LLM_URL not set: reservas handler disabled, it needs the LLM to read messages")
	}
	rt := router.New(store, handlers, log)

	wa, err := whatsapp.New(ctx, filepath.Join(cfg.DataDir, "whatsapp.db"), rt.Dispatch, log)
	if err != nil {
		return err
	}
	if err := wa.Start(ctx); err != nil {
		return fmt.Errorf("start whatsapp: %w", err)
	}
	defer wa.Stop()
	sender.wa = wa
	if booker != nil {
		go booker.Run(ctx)
	}
	go site.KeepAlive(ctx, 5*time.Minute)

	mux := http.NewServeMux()
	mux.Handle("/", dash.New(wa, store, rt.HandlerNames(), log).WithSuperlogica(condo).Handler())
	mux.Handle("POST /notify", notify.New(wa, store, cfg.NotifyToken, documents.Name, log))

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.ListenAndServe() }()
	log.Info("dashboard listening", "addr", cfg.Listen)

	select {
	case <-ctx.Done():
	case <-wa.LoggedOut:
		// Unlinked from the phone: exit so a restart starts pairing again.
		err = errors.New("whatsapp session was logged out")
	case err = <-srvErr:
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	return err
}

// lateSender lets the reservas handler post to groups; the WhatsApp client
// it needs is created after the handler.
type lateSender struct{ wa *whatsapp.Client }

func (l *lateSender) Send(ctx context.Context, chatJID, text string) error {
	if l.wa == nil {
		return errors.New("whatsapp not started")
	}
	return l.wa.Send(ctx, chatJID, text)
}
