package superlogica

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// DefaultSSOHost is the Keycloak host that holds the long-lived session.
const DefaultSSOHost = "sso.superlogica.com"

// ErrNoSSOSession means the Keycloak session is gone too, so nothing can be
// renewed without someone logging in by hand and pasting a session in the
// dashboard.
var ErrNoSSOSession = errors.New("keycloak SSO session is gone: log in by hand and paste the session in the dashboard")

// SSOLogin renews the app session through the Keycloak SSO session that the
// saved session already carries.
//
// The login form is behind reCAPTCHA Enterprise and is never submitted. It
// doesn't have to be: the site keeps two sessions, and only the short one
// expires.
//
//	app session   PHPSESSID on the condo host      short
//	SSO session   cookies on sso.superlogica.com   much longer
//
// Opening the condo site with the Keycloak cookies still attached makes the
// OIDC authorize endpoint redirect straight back to the callback instead of
// rendering a form, which mints a new app session. No form, no reCAPTCHA.
//
// The flow is started from the condo site rather than by building an
// authorize URL here on purpose: the site keeps the PKCE verifier for the
// code it is about to be handed, so only its own request can be exchanged.
type SSOLogin struct {
	client  *Client // the saved session, which holds the Keycloak cookies
	SSOHost string
}

func NewSSOLogin(c *Client) *SSOLogin {
	return &SSOLogin{client: c, SSOHost: DefaultSSOHost}
}

func (l *SSOLogin) Login(ctx context.Context) ([]Cookie, error) {
	base := l.client.base
	sso := CookiesForHost(l.client.Session(), l.SSOHost)
	if len(sso) == 0 {
		return nil, fmt.Errorf("%w (no %s cookies in the saved session)", ErrNoSSOSession, l.SSOHost)
	}

	jar, err := ssoJar(sso, l.SSOHost)
	if err != nil {
		return nil, err
	}
	var chain []string
	http2 := &http.Client{
		Jar:     jar,
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			chain = append(chain, req.URL.Host+req.URL.Path)
			if len(via) >= 15 {
				return fmt.Errorf("too many redirects: %s", strings.Join(chain, " -> "))
			}
			return nil
		},
	}

	body, final, err := ssoGet(ctx, http2, base.String()+"/clients/areadocondomino")
	if err != nil {
		return nil, err
	}
	// Keycloak renders the form instead of redirecting when its session is
	// gone, either at its own host or after bouncing back.
	if final.Host == l.SSOHost || strings.Contains(body, "kc-form-login") {
		return nil, ErrNoSSOSession
	}
	if final.Host != base.Host {
		return nil, fmt.Errorf("ended up on %s, not %s (%s)", final.Host, base.Host, strings.Join(chain, " -> "))
	}

	// Prove the session works before handing it back, so a bad one is
	// reported here rather than failing at the next booking.
	check, _, err := ssoGet(ctx, http2, base.String()+"/clients/areadocondomino/reservas")
	if err != nil {
		return nil, err
	}
	if !strings.Contains(check, "Reservas online") {
		return nil, fmt.Errorf("the new session doesn't open the reservations page (%s)", strings.Join(chain, " -> "))
	}

	// Both hosts: the Keycloak cookies rotate, and keeping them is what makes
	// the next renewal possible.
	cookies := collectCookies(jar, base.Host, l.SSOHost)
	if len(cookies) == 0 {
		return nil, errors.New("logged in but no cookies came back")
	}
	return cookies, nil
}

func ssoGet(ctx context.Context, c *http.Client, rawURL string) (string, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "pt-BR,pt;q=0.9")
	resp, err := c.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", nil, err
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
		return "", nil, fmt.Errorf("%w: %s answered HTTP %d", ErrBlocked, resp.Request.URL.Host, resp.StatusCode)
	}
	return string(body), resp.Request.URL, nil
}

func ssoJar(cookies []Cookie, fallbackHost string) (http.CookieJar, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	byHost := map[string][]*http.Cookie{}
	for _, ck := range cookies {
		host := strings.TrimPrefix(ck.Domain, ".")
		if host == "" {
			host = fallbackHost
		}
		path := ck.Path
		if path == "" {
			path = "/"
		}
		// Host-only on purpose: the jar matches Domain against the port-less
		// request host and silently drops anything that doesn't line up, and
		// the host each cookie came from is already known.
		byHost[host] = append(byHost[host], &http.Cookie{Name: ck.Name, Value: ck.Value, Path: path})
	}
	for host, cs := range byHost {
		jar.SetCookies(&url.URL{Scheme: "https", Host: host, Path: "/"}, cs)
	}
	return jar, nil
}

// collectCookies reads every cookie the jar holds for these hosts. net/http's
// jar only answers per request URL, so each host is asked about the paths the
// flow uses; a cookie scoped to a subpath is invisible at "/".
func collectCookies(jar http.CookieJar, hosts ...string) []Cookie {
	paths := []string{"/", "/clients/areadocondomino", "/areadocondomino", "/auth/realms/superlogica-cloud"}
	var out []Cookie
	for _, host := range hosts {
		seen := map[string]bool{}
		for _, p := range paths {
			for _, ck := range jar.Cookies(&url.URL{Scheme: "https", Host: host, Path: p}) {
				if seen[ck.Name] {
					continue
				}
				seen[ck.Name] = true
				out = append(out, Cookie{Name: ck.Name, Value: ck.Value, Domain: host, Path: "/"})
			}
		}
	}
	return out
}

// CookiesForHost picks the cookies belonging to one host.
func CookiesForHost(cookies []Cookie, host string) []Cookie {
	var out []Cookie
	for _, ck := range cookies {
		if strings.TrimPrefix(ck.Domain, ".") == host {
			out = append(out, ck)
		}
	}
	return out
}
