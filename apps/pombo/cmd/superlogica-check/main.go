// superlogica-check tells whether a session (a cookie list from a login
// service, or the one pombo saved) is logged in, and can make a test booking
// and cancel it right away.
//
//	go run ./cmd/superlogica-check -base https://... -condo .. -unit .. -session cookies.json
//	go run ./cmd/superlogica-check -login-url http://localhost:8000/login
//	go run ./cmd/superlogica-check -session cookies.json -book 788 -date 2026-12-20
//	go run ./cmd/superlogica-check -login-url ... -book 788 -date 2026-12-20 -keep
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/superlogica"
)

func main() {
	base := flag.String("base", "", "condo site, e.g. https://your-condo.superlogica.net")
	condo := flag.String("condo", "", "condo ID")
	unit := flag.String("unit", "", "unit ID")
	session := flag.String("session", "", "JSON cookie list to check")
	loginURL := flag.String("login-url", "", "login service to get a session from instead (POST)")
	area := flag.String("book", "", "area ID to book and then cancel, to test booking")
	date := flag.String("date", "", "date for -book, YYYY-MM-DD")
	keep := flag.Bool("keep", false, "with -book, leave the booking in place instead of cancelling it")
	flag.Parse()
	if err := run(*base, *condo, *unit, *session, *loginURL, *area, *date, *keep); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

func run(base, condo, unit, sessionPath, loginURL, area, date string, keep bool) error {
	if base == "" || condo == "" || unit == "" {
		return fmt.Errorf("-base, -condo and -unit are required")
	}
	ctx := context.Background()
	c, err := superlogica.New(base, condo, unit, "")
	if err != nil {
		return err
	}
	var cookies []superlogica.Cookie
	switch {
	case loginURL != "":
		start := time.Now()
		if cookies, err = superlogica.NewHTTPLogin(loginURL).Login(ctx); err != nil {
			return err
		}
		fmt.Printf("login service returned %d cookies in %s\n", len(cookies), time.Since(start).Round(time.Second))
	case sessionPath != "":
		data, err := os.ReadFile(sessionPath)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &cookies); err != nil {
			return fmt.Errorf("%s is not a cookie list: %w", sessionPath, err)
		}
	default:
		return fmt.Errorf("pass -session or -login-url")
	}
	if err := c.SetSession(cookies); err != nil {
		return err
	}
	if err := c.Alive(ctx); err != nil {
		return err
	}
	fmt.Println("OK: session is logged in")

	if area == "" {
		return nil
	}
	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		return fmt.Errorf("-date: %w", err)
	}
	r, err := c.Book(ctx, superlogica.Area{ID: area}, day)
	if err != nil {
		return err
	}
	if r.Outcome != superlogica.Booked {
		return fmt.Errorf("not booked: %s", r.Msg)
	}
	if keep {
		fmt.Printf("OK: booked (reservation %s, queue %s); left in place, cancel it on the site\n", r.ID, r.Queue)
		return nil
	}
	fmt.Printf("OK: booked (reservation %s, queue %s); cancelling\n", r.ID, r.Queue)
	if err := c.Cancel(ctx, r.ID, day, "Teste do pombo"); err != nil {
		return fmt.Errorf("booked %s but could not cancel, cancel it on the site: %w", r.ID, err)
	}
	fmt.Println("OK: cancelled")
	return nil
}
