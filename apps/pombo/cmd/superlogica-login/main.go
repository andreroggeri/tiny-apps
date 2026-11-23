// superlogica-login renews a condo-site session on its own, outside pombo.
//
// Pombo renews sessions in-process (internal/superlogica.SSOLogin), so this
// is not deployed. It stays as the hand tool for seeding a session from a
// browser and for checking the renewal without running pombo.
//
//	go run ./cmd/superlogica-login -seed curl.txt   # from "Copy as cURL"
//	go run ./cmd/superlogica-login                  # serve POST /login
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/superlogica"
)

type config struct {
	Listen      string // address to serve POST /login on
	Base        string // condo site
	CondoID     string
	UnitID      string
	SSOHost     string // Keycloak host holding the long-lived session
	SessionPath string // where the session cookies live

	Email      string // only for the emailed-code path, which is unused
	OTPURL     string
	OTPToken   string
	OTPTimeout time.Duration
}

func main() {
	seed := flag.String("seed", "", `seed the session from this file: a JSON cookie list or a "Copy as cURL" command`)
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := config{
		Listen:      getenv("LISTEN", ":8000"),
		Base:        os.Getenv("SUPERLOGICA_URL"),
		CondoID:     os.Getenv("SUPERLOGICA_CONDO_ID"),
		UnitID:      os.Getenv("SUPERLOGICA_UNIT_ID"),
		SSOHost:     getenv("SUPERLOGICA_SSO_HOST", superlogica.DefaultSSOHost),
		SessionPath: getenv("SUPERLOGICA_SESSION_PATH", "superlogica-session.json"),
		Email:       os.Getenv("SUPERLOGICA_EMAIL"),
		OTPURL:      os.Getenv("OTP_RELAY_URL"),
		OTPToken:    os.Getenv("OTP_RELAY_TOKEN"),
		OTPTimeout:  3 * time.Minute,
	}

	if *seed != "" {
		n, err := seedSession(*seed, cfg.SessionPath, cfg.SSOHost)
		if err != nil {
			log.Error("seeding failed", "err", err)
			os.Exit(1)
		}
		log.Info("seeded", "cookies", n, "from", *seed, "to", cfg.SessionPath)
		return
	}

	if cfg.Base == "" || cfg.CondoID == "" || cfg.UnitID == "" {
		log.Error("SUPERLOGICA_URL, SUPERLOGICA_CONDO_ID and SUPERLOGICA_UNIT_ID are required")
		os.Exit(1)
	}
	client, err := superlogica.New(cfg.Base, cfg.CondoID, cfg.UnitID, cfg.SessionPath)
	if err != nil {
		log.Error("cannot open the saved session", "err", err)
		os.Exit(1)
	}
	if !client.HasSession() {
		log.Warn("no session saved yet; seed one with -seed", "path", cfg.SessionPath)
	}
	sso := superlogica.NewSSOLogin(client)
	sso.SSOHost = cfg.SSOHost

	var oneAtATime sync.Mutex
	http.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		if !oneAtATime.TryLock() {
			http.Error(w, "another login is running", http.StatusConflict)
			return
		}
		defer oneAtATime.Unlock()
		start := time.Now()
		cookies, err := sso.Login(r.Context())
		if err != nil {
			log.Warn("login failed", "err", err, "took", time.Since(start).Round(time.Second))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Keep the rotated Keycloak cookies, or the next renewal uses stale ones.
		if err := client.SetSession(cookies); err != nil {
			log.Warn("could not save the renewed session", "err", err)
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

// seedSession writes the Keycloak cookies from a hand-made browser login to
// the session file. The file is either a JSON cookie list or a "Copy as cURL"
// command from the browser's network panel, which is the easier one to make:
// the Keycloak cookies are HttpOnly, so no console snippet can read them.
func seedSession(from, to, ssoHost string) (int, error) {
	data, err := os.ReadFile(from)
	if err != nil {
		return 0, err
	}
	var cookies []superlogica.Cookie
	if looksLikeCurl(data) {
		host, parsed, err := parseCurl(string(data))
		if err != nil {
			return 0, fmt.Errorf("%s: %w", from, err)
		}
		if host != ssoHost {
			return 0, fmt.Errorf("%s is a request to %s, which never carries the Keycloak cookies; copy a request to %s instead", from, host, ssoHost)
		}
		cookies = parsed
	} else if err := json.Unmarshal(data, &cookies); err != nil {
		return 0, fmt.Errorf("%s is neither a cookie list nor a cURL command: %w", from, err)
	}
	sso := superlogica.CookiesForHost(cookies, ssoHost)
	if len(sso) == 0 {
		return 0, fmt.Errorf("no %s cookies in %s: export the whole browser session, both domains", ssoHost, from)
	}
	out, err := json.Marshal(sso)
	if err != nil {
		return 0, err
	}
	tmp := to + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return 0, err
	}
	return len(sso), os.Rename(tmp, to)
}

func looksLikeCurl(data []byte) bool {
	return strings.HasPrefix(strings.TrimSpace(strings.ToLower(string(data))), "curl")
}

var (
	curlURL    = regexp.MustCompile(`['"](https?://[^'"]+)['"]`)
	curlCookie = regexp.MustCompile(`(?is)(?:-H|--header)\s+['"]cookie:\s*([^'"]*)['"]`)
	curlShortB = regexp.MustCompile(`(?is)(?:-b|--cookie)\s+['"]([^'"]*=[^'"]*)['"]`)
)

// parseCurl pulls the host and the Cookie header out of a "Copy as cURL"
// command. Both the -H 'Cookie: ...' form (Chrome, Firefox) and -b are read.
func parseCurl(text string) (string, []superlogica.Cookie, error) {
	m := curlURL.FindStringSubmatch(text)
	if m == nil {
		return "", nil, errors.New("no URL in the cURL command")
	}
	u, err := url.Parse(m[1])
	if err != nil {
		return "", nil, fmt.Errorf("the cURL command's URL: %w", err)
	}

	raw := ""
	if c := curlCookie.FindStringSubmatch(text); c != nil {
		raw = c[1]
	} else if c := curlShortB.FindStringSubmatch(text); c != nil {
		raw = c[1]
	}
	if strings.TrimSpace(raw) == "" {
		return u.Host, nil, fmt.Errorf("no Cookie header in the cURL command for %s", u.Host)
	}

	var cookies []superlogica.Cookie
	for _, part := range strings.Split(raw, ";") {
		name, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found || name == "" {
			continue
		}
		cookies = append(cookies, superlogica.Cookie{Name: name, Value: value, Domain: u.Host, Path: "/"})
	}
	return u.Host, cookies, nil
}

// waitForCode polls the otp-relay Worker until an email that arrived after
// since has a code, or cfg.OTPTimeout passes.
//
// Unused: the renewal path never submits the login form, so no code is ever
// sent. Kept against the day reCAPTCHA makes the form unavoidable. If that
// day doesn't come, this and the otp-relay Worker can go; see
// login/OTP-RELAY.md.
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
