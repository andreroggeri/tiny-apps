// Package superlogica books areas of the condo (e.g. the manicure chair) on
// Superlogica's "Área do Condômino" website.
//
// The site has no public API. These are the same requests its own pages make,
// sent with a logged-in session's cookies. Logging in is not done here: a
// session comes from a Login (see login.go) or is pasted in the dashboard.
package superlogica

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var (
	// ErrSessionExpired means the site sent us to the login page: a new
	// session is needed.
	ErrSessionExpired = errors.New("superlogica session expired")
	// ErrBlocked means the site answered 401, 403 or 429. Stop instead of
	// retrying, so the account isn't hammered.
	ErrBlocked = errors.New("superlogica blocked the request")
)

// Outcome is what the site said about a booking attempt.
type Outcome int

const (
	Booked      Outcome = iota // the slot is ours now
	NotOpen                    // the date isn't open for bookings yet
	AlreadyOurs                // this unit already holds it
	Rejected                   // anything else (taken, rule broken, ...): Msg says why
)

type Result struct {
	Outcome Outcome
	ID      string // reservation ID, when Booked
	Queue   string // position in the waiting list, when Booked ("1" = first)
	Msg     string // the site's message
}

// Area is a bookable thing on the site. Each 2-hour slot of a chair is its
// own area.
type Area struct {
	ID   string
	Name string // as the site names it, sent along with bookings
}

// Cookie is one session cookie. The JSON shape is what Playwright (and
// Camoufox) return from context.cookies(), so a login script can hand its
// cookies over unchanged. Extra fields are ignored.
type Cookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
	Path   string `json:"path"`
}

type Client struct {
	base        *url.URL
	condoID     string
	unitID      string
	sessionPath string // where the session cookies are saved, to survive restarts

	mu      sync.Mutex
	http    *http.Client
	cookies []Cookie
}

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"

// New returns a client for the condo site at base (e.g.
// https://kpgadministrado.superlogica.net). A session saved at sessionPath
// by an earlier run is loaded; a missing file just means no session yet.
func New(base, condoID, unitID, sessionPath string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return nil, fmt.Errorf("superlogica base URL: %w", err)
	}
	c := &Client{base: u, condoID: condoID, unitID: unitID, sessionPath: sessionPath}
	c.http = newHTTP(nil, u)
	if sessionPath == "" {
		return c, nil
	}
	data, err := os.ReadFile(sessionPath)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	var cookies []Cookie
	if err := json.Unmarshal(data, &cookies); err != nil {
		return nil, fmt.Errorf("saved session %s: %w", sessionPath, err)
	}
	c.useCookies(cookies)
	return c, nil
}

func newHTTP(cookies []Cookie, base *url.URL) *http.Client {
	jar, _ := cookiejar.New(nil)
	byHost := map[string][]*http.Cookie{}
	for _, ck := range cookies {
		host := strings.TrimPrefix(ck.Domain, ".")
		if host == "" {
			host = base.Host
		}
		path := ck.Path
		if path == "" {
			path = "/"
		}
		byHost[host] = append(byHost[host], &http.Cookie{Name: ck.Name, Value: ck.Value, Path: path, Domain: ck.Domain})
	}
	for host, cs := range byHost {
		jar.SetCookies(&url.URL{Scheme: "https", Host: host, Path: "/"}, cs)
	}
	return &http.Client{Jar: jar, Timeout: 20 * time.Second}
}

func (c *Client) useCookies(cookies []Cookie) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cookies = cookies
	c.http = newHTTP(cookies, c.base)
}

// SetSession replaces the session with these cookies and saves them. It
// doesn't check them; call Alive for that.
func (c *Client) SetSession(cookies []Cookie) error {
	if len(cookies) == 0 {
		return errors.New("no cookies in session")
	}
	c.useCookies(cookies)
	if c.sessionPath == "" {
		return nil
	}
	data, _ := json.Marshal(cookies)
	tmp := c.sessionPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.sessionPath)
}

// HasSession reports whether any session cookies are loaded.
func (c *Client) HasSession() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.cookies) > 0
}

func (c *Client) client() *http.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.http
}

// Alive loads the reservations page. It returns nil if the session is
// logged in, ErrSessionExpired if the site wants a login. Calling it every
// few minutes also keeps the session from idling out.
func (c *Client) Alive(ctx context.Context) error {
	if !c.HasSession() {
		return ErrSessionExpired
	}
	resp, body, err := c.do(ctx, http.MethodGet, "/clients/areadocondomino/reservas", "", "")
	if err != nil {
		return err
	}
	if resp.Request.URL.Host != c.base.Host || !strings.Contains(body, "Reservas online") {
		return ErrSessionExpired
	}
	return nil
}

// Book tries to reserve area on date (only the day counts).
func (c *Client) Book(ctx context.Context, area Area, date time.Time) (Result, error) {
	return c.post(ctx, "/areadocondomino/atual/reservas/put", map[string]string{
		"nomeArea":            area.Name,
		"ID_UNIDADE_UNI":      c.unitID,
		"FL_BLOQUEARINAD_ARE": "0",
		"existeReserva":       "",
		"flVerregras":         "",
		"ID_CONDOMINIO_COND":  c.condoID,
		"FL_REGRAS_ARE":       "1", // "I agree to the rules"
		"ST_UNIDADE_UNI":      "",
		"ID_AREA_ARE":         area.ID,
		"DT_RESERVA_RES":      siteDate(date),
		"FL_COBRANCA_ARE":     "0",
	})
}

// Cancel cancels a reservation made by Book.
func (c *Client) Cancel(ctx context.Context, reservationID string, date time.Time, reason string) error {
	if len([]rune(reason)) < 5 {
		reason = "Cancelado pelo pombo" // the site wants at least 5 characters
	}
	r, err := c.post(ctx, "/areadocondomino/atual/reservas/cancelar", map[string]string{
		"ID_RESERVA_RES":                         reservationID,
		"ID_CONDOMINIO_COND":                     c.condoID,
		"ST_MOTIVOCANCELAMENTO_RES":              reason,
		"DT_RESERVA_RES":                         siteDate(date),
		"FL_STATUS_RES":                          "1",
		"NM_ANTECEDENCIAMINIMACANCELAMENTO_AREA": "0",
	})
	if err != nil {
		return err
	}
	if r.Outcome != Booked { // "Booked" here just means status 200
		return fmt.Errorf("cancel %s: %s", reservationID, r.Msg)
	}
	return nil
}

func siteDate(d time.Time) string { return d.Format("01/02/2006") + " 00:00:00" }

func (c *Client) post(ctx context.Context, path string, params map[string]string) (Result, error) {
	endpoint := c.base.String() + path
	inner := make(map[string]string, len(params))
	for k, v := range params {
		inner[k] = quote(v)
	}
	payload, _ := json.Marshal(map[string]any{"params": []any{inner}, "url": endpoint})
	resp, body, err := c.do(ctx, http.MethodPost, path, "json="+quote(string(payload)), "application/x-www-form-urlencoded; charset=UTF-8")
	if err != nil {
		return Result{}, err
	}
	if resp.Request.URL.Host != c.base.Host {
		return Result{}, ErrSessionExpired
	}
	return parseResult(body)
}

func (c *Client) do(ctx context.Context, method, path, body, contentType string) (*http.Response, string, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, strings.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
		return nil, "", fmt.Errorf("%w: HTTP %d on %s", ErrBlocked, resp.StatusCode, path)
	}
	return resp, string(data), nil
}

// parseResult reads the site's answer. Errors come back as HTTP 200 too:
//
//	{"status":"206","msg":"...","data":[{"status":"500","msg":"A área ainda não está disponível para reservas."}]}
//
// and a booking as data[0].status 200 with data[0].data.id_reserva_res.
func parseResult(body string) (Result, error) {
	var top struct {
		Status any               `json:"status"`
		Msg    string            `json:"msg"`
		Data   []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &top); err != nil {
		// The login page is HTML, so this is how an expired session shows up.
		return Result{}, ErrSessionExpired
	}
	status, msg := fmt.Sprint(top.Status), top.Msg
	var detail map[string]any
	if len(top.Data) > 0 {
		var item struct {
			Status any            `json:"status"`
			Msg    string         `json:"msg"`
			Data   map[string]any `json:"data"`
		}
		if json.Unmarshal(top.Data[0], &item) == nil {
			status, msg, detail = fmt.Sprint(item.Status), item.Msg, item.Data
		}
	}
	r := Result{Msg: msg}
	plain := simplify(msg)
	switch {
	case status == "200":
		r.Outcome = Booked
		if detail != nil {
			r.ID = str(detail["id_reserva_res"])
			r.Queue = str(detail["nm_fila_res"])
		}
	case strings.Contains(plain, "nao esta disponivel"):
		r.Outcome = NotOpen
	case strings.Contains(plain, "reservou nesse"):
		r.Outcome = AlreadyOurs
	default:
		r.Outcome = Rejected
	}
	return r, nil
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// simplify lowercases s and drops accents, so "não está" matches "nao esta".
func simplify(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// quote percent-encodes like Python's urllib.parse.quote (keeps letters,
// digits, "_.-~" and "/"), which is what the site's own JavaScript produces.
func quote(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '_', c == '.', c == '-', c == '~', c == '/':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
