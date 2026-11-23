package superlogica

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// ErrNoLogin means no login service is configured, so a session can only
// come from the dashboard.
var ErrNoLogin = errors.New("no superlogica login configured")

// Login gets a fresh logged-in session.
type Login interface {
	Login(ctx context.Context) ([]Cookie, error)
}

// NoLogin is used when SUPERLOGICA_LOGIN_URL is unset.
type NoLogin struct{}

func (NoLogin) Login(context.Context) ([]Cookie, error) { return nil, ErrNoLogin }

// HTTPLogin asks a separate login service for a session. The service is not
// part of pombo; see login/README.md for what it must do.
//
// Contract: pombo sends POST <url> with no body and waits up to 5 minutes
// (logging in includes waiting for the emailed code). The service answers
// 200 with a JSON array of cookies, in Playwright's context.cookies() shape:
//
//	[{"name":"PHPSESSID","value":"...","domain":"your-condo.superlogica.net","path":"/"}, ...]
//
// Anything else is a failed login; the body is shown in the logs.
type HTTPLogin struct {
	URL  string
	HTTP *http.Client
}

func NewHTTPLogin(url string) *HTTPLogin {
	return &HTTPLogin{URL: url, HTTP: &http.Client{Timeout: 5 * time.Minute}}
}

func (l *HTTPLogin) Login(ctx context.Context) ([]Cookie, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := l.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("login service: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("login service: HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	var cookies []Cookie
	if err := json.Unmarshal(body, &cookies); err != nil {
		return nil, fmt.Errorf("login service: response is not a cookie list: %w", err)
	}
	if len(cookies) == 0 {
		return nil, errors.New("login service returned no cookies")
	}
	return cookies, nil
}

// Site is a client plus the way to get a new session when it expires.
type Site struct {
	*Client
	login Login
	log   *slog.Logger
}

func NewSite(c *Client, login Login, log *slog.Logger) *Site {
	return &Site{Client: c, login: login, log: log}
}

// Ready makes sure there's a working session, logging in if needed.
func (s *Site) Ready(ctx context.Context) error { return s.EnsureSession(ctx, s.login) }

// KeepAlive checks the session every interval, which keeps it from idling
// out, and logs in again when it expires. It logs how long each session
// lasted, to learn how often a login is needed.
func (s *Site) KeepAlive(ctx context.Context, every time.Duration) {
	var since time.Time // when the current session was first seen working
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		err := s.Alive(ctx)
		switch {
		case err == nil:
			if since.IsZero() {
				since = time.Now()
				s.log.Info("superlogica session is logged in")
			}
		case errors.Is(err, ErrSessionExpired):
			if !since.IsZero() {
				s.log.Warn("superlogica session expired", "lasted_at_least", time.Since(since).Round(time.Minute))
				since = time.Time{}
			}
			if rerr := s.Ready(ctx); rerr == nil {
				since = time.Now()
				s.log.Info("superlogica session renewed")
			} else if !errors.Is(rerr, ErrNoLogin) {
				s.log.Warn("superlogica login failed", "err", rerr)
			}
		default:
			s.log.Warn("superlogica keep-alive failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// EnsureSession checks the session and, if it expired, gets a new one from
// login. It returns ErrSessionExpired (wrapped with the login error) when
// no working session could be had.
func (c *Client) EnsureSession(ctx context.Context, login Login) error {
	err := c.Alive(ctx)
	if !errors.Is(err, ErrSessionExpired) {
		return err
	}
	cookies, lerr := login.Login(ctx)
	if lerr != nil {
		return fmt.Errorf("%w (login: %v)", ErrSessionExpired, lerr)
	}
	if err := c.SetSession(cookies); err != nil {
		return err
	}
	if err := c.Alive(ctx); err != nil {
		return fmt.Errorf("new session from login doesn't work: %w", err)
	}
	return nil
}
