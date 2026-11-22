// superlogica-login is the login service pombo calls when its condo-site
// session expires (SUPERLOGICA_LOGIN_URL). It is a separate binary so the
// browser it needs doesn't end up in pombo's image. Contract: login/README.md.
//
// NOT IMPLEMENTED: login() below is the part left to write. The HTTP server
// and reading the emailed code from the otp-relay Worker are done.
//
//	SUPERLOGICA_EMAIL=...  OTP_RELAY_TOKEN=...  go run ./cmd/superlogica-login
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/superlogica"
)

type config struct {
	Listen     string // address to serve POST /login on
	Email      string // account to log in as; the code is emailed here
	OTPURL     string // otp-relay endpoint for this mailbox
	OTPToken   string // bearer token for it
	OTPTimeout time.Duration
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := config{
		Listen:     getenv("LISTEN", ":8000"),
		Email:      os.Getenv("SUPERLOGICA_EMAIL"),
		OTPURL:     getenv("OTP_RELAY_URL", "https://otp.roggeri.com.br/codes/superlogica"),
		OTPToken:   os.Getenv("OTP_RELAY_TOKEN"),
		OTPTimeout: 3 * time.Minute,
	}
	if cfg.Email == "" || cfg.OTPToken == "" {
		log.Error("SUPERLOGICA_EMAIL and OTP_RELAY_TOKEN are required")
		os.Exit(1)
	}

	var oneAtATime sync.Mutex
	http.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		if !oneAtATime.TryLock() {
			http.Error(w, "another login is running", http.StatusConflict)
			return
		}
		defer oneAtATime.Unlock()
		start := time.Now()
		cookies, err := login(r.Context(), cfg)
		if err != nil {
			log.Warn("login failed", "err", err, "took", time.Since(start).Round(time.Second))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Info("login ok", "cookies", len(cookies), "took", time.Since(start).Round(time.Second))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cookies)
	})
	log.Info("listening", "addr", cfg.Listen)
	if err := http.ListenAndServe(cfg.Listen, nil); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

// login logs in to the condo site and returns the browser's cookies.
//
// TODO: implement. Steps the site needs:
//  1. Open https://kpgadministrado.superlogica.net/clients/areadocondomino
//     (it redirects to sso.superlogica.com).
//  2. Note the time, fill cfg.Email and continue; the site emails a code.
//  3. code, err := waitForCode(ctx, cfg, askedAt) and fill it in.
//  4. Wait until the page is back on
//     kpgadministrado.superlogica.net/clients/areadocondomino/...
//  5. Return all cookies (both domains), as superlogica.Cookie values.
//
// Check it with: go run ./cmd/superlogica-check -login-url http://localhost:8000/login
func login(ctx context.Context, cfg config) ([]superlogica.Cookie, error) {
	return nil, errors.New("superlogica login is not implemented yet")
}

// waitForCode polls the otp-relay Worker until an email that arrived after
// since has a code, or cfg.OTPTimeout passes.
func waitForCode(ctx context.Context, cfg config, since time.Time) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.OTPTimeout)
	defer cancel()
	url := fmt.Sprintf("%s?since=%d", cfg.OTPURL, since.UnixMilli())
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+cfg.OTPToken)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			var body struct {
				Code  *string `json:"code"`
				Error string  `json:"error"`
			}
			json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			switch {
			case resp.StatusCode == http.StatusOK && body.Code != nil:
				return *body.Code, nil
			case resp.StatusCode == http.StatusOK:
				return "", errors.New("code email arrived but no code was found in it")
			case resp.StatusCode == http.StatusUnauthorized:
				return "", errors.New("otp-relay refused the token")
			}
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("no code email within %s", cfg.OTPTimeout)
		case <-time.After(2 * time.Second):
		}
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
