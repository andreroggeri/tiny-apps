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
	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/notify"
	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/paperless"
	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/router"
	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/settings"
	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/whatsapp"
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
	}
	if c.PaperlessURL == "" || c.PaperlessToken == "" {
		return c, errors.New("PAPERLESS_URL and PAPERLESS_TOKEN are required")
	}
	if c.LLMURL != "" && c.LLMModel == "" {
		return c, errors.New("LLM_MODEL is required when LLM_URL is set")
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
	rt := router.New(store, map[string]router.Handler{
		documents.Name: docs,
	}, log)

	wa, err := whatsapp.New(ctx, filepath.Join(cfg.DataDir, "whatsapp.db"), rt.Dispatch, log)
	if err != nil {
		return err
	}
	if err := wa.Start(ctx); err != nil {
		return fmt.Errorf("start whatsapp: %w", err)
	}
	defer wa.Stop()

	mux := http.NewServeMux()
	mux.Handle("/", dash.New(wa, store, rt.HandlerNames(), log).Handler())
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
