// Command coruja summarizes new paperless documents and tells pombo about
// them. paperless calls it through a workflow webhook.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/andreroggeri/tiny-apps/apps/coruja/internal/app"
	"github.com/andreroggeri/tiny-apps/apps/coruja/internal/llm"
	"github.com/andreroggeri/tiny-apps/apps/coruja/internal/paperless"
)

// Custom fields in paperless, created by hand once.
const (
	summaryField = "Resumo"
	sentByField  = "Enviado por" // set by pombo on upload
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func run(log *slog.Logger) error {
	var missing []string
	for _, name := range []string{"PAPERLESS_URL", "PAPERLESS_TOKEN", "LLM_URL", "LLM_MODEL", "WEBHOOK_TOKEN"} {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing environment variables: %v", missing)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pl := paperless.New(os.Getenv("PAPERLESS_URL"), os.Getenv("PAPERLESS_TOKEN"))
	summaryID, err := pl.CustomFieldID(ctx, summaryField)
	if err != nil {
		return err
	}
	sentByID, err := pl.CustomFieldID(ctx, sentByField)
	if err != nil {
		log.Warn("notifications won't say who sent the document", "err", err)
	}

	var notifier app.Notifier
	if u := os.Getenv("NOTIFY_URL"); u != "" {
		notifier = &app.HTTPNotifier{URL: u, Token: os.Getenv("NOTIFY_TOKEN"), HTTP: &http.Client{Timeout: 30 * time.Second}}
	} else {
		log.Warn("NOTIFY_URL not set: documents are summarized, nobody is told")
	}

	a := app.New(pl, llm.New(os.Getenv("LLM_URL"), os.Getenv("LLM_MODEL")), notifier, log)
	a.Token = os.Getenv("WEBHOOK_TOKEN")
	a.SummaryField, a.SentByField = summaryID, sentByID
	a.PublicURL = env("PAPERLESS_PUBLIC_URL", os.Getenv("PAPERLESS_URL"))
	go a.Run(ctx)

	srv := &http.Server{Addr: env("LISTEN", ":8080"), Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.ListenAndServe() }()
	log.Info("listening", "addr", srv.Addr)

	select {
	case <-ctx.Done():
	case err = <-srvErr:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	return err
}
