package reservas

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/router"
	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/superlogica"
)

type fakeSite struct {
	mu       sync.Mutex
	ready    error
	opensAt  time.Time         // NotOpen before this (per the fake clock)
	refuse   map[string]string // area ID -> refusal message
	booked   []string          // area IDs, in order
	clock    func() time.Time
	attempts map[string]int
}

func (f *fakeSite) Ready(context.Context) error { return f.ready }

func (f *fakeSite) Book(_ context.Context, a superlogica.Area, _ time.Time) (superlogica.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attempts == nil {
		f.attempts = map[string]int{}
	}
	f.attempts[a.ID]++
	if f.clock().Before(f.opensAt) {
		return superlogica.Result{Outcome: superlogica.NotOpen, Msg: "A área ainda não está disponível para reservas."}, nil
	}
	if msg, ok := f.refuse[a.ID]; ok {
		return superlogica.Result{Outcome: superlogica.Rejected, Msg: msg}, nil
	}
	f.booked = append(f.booked, a.ID)
	return superlogica.Result{Outcome: superlogica.Booked, ID: "1", Queue: "1"}, nil
}

type sent struct {
	mu    sync.Mutex
	texts []string
}

func (s *sent) Send(_ context.Context, _ string, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.texts = append(s.texts, text)
	return nil
}

func (s *sent) all() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.texts, "\n")
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) set(t time.Time) { c.mu.Lock(); defer c.mu.Unlock(); c.t = t }

var sp, _ = time.LoadLocation("America/Sao_Paulo")

// fakeLLM answers from a table, as the model would.
type fakeLLM struct {
	answers   map[string]parsed
	questions []string // what the bot had asked, per call
}

func (f *fakeLLM) interpret(_ context.Context, _ time.Time, _ *draft, question, text string) (parsed, error) {
	f.questions = append(f.questions, question)
	return f.answers[text], nil
}

func day(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, sp) }

var answers = map[string]parsed{
	"unha sábado 15h":              {related: true, mentionsChair: true, date: day(11, 28), start: 15},
	"18h":                          {related: true, end: 18},
	"unha 05/12 14h às 18h":        {related: true, mentionsChair: true, date: day(12, 5), start: 14, end: 18},
	"unha amanhã 21h às 23h":       {related: true, mentionsChair: true, date: day(11, 23), start: 21, end: 23},
	"unha 09/01 9h às 12h":         {related: true, mentionsChair: true, date: time.Date(2027, 1, 9, 0, 0, 0, 0, sp), start: 9, end: 12},
	"o que tem agendado?":          {related: true, list: true},
	"esquece":                      {related: true, cancel: true},
	"bom dia, alguém viu a chave?": {},
}

func setup(t *testing.T, now time.Time) (*Handler, *fakeSite, *sent, *clock) {
	t.Helper()
	clk := &clock{t: now}
	site := &fakeSite{clock: clk.now, refuse: map[string]string{}}
	store, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "reservas.db"), sp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	out := &sent{}
	h := New(site, store, out, nil, sp, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.llm = &fakeLLM{answers: answers}
	h.now = clk.now
	h.pollEvery = time.Millisecond
	return h, site, out, clk
}

// say sends text and returns the bot's replies.
func say(t *testing.T, h *Handler, text string) []string {
	t.Helper()
	var replies []string
	m := router.Message{ChatJID: "group", SenderPhone: "5511", Text: text,
		ReplyFunc: func(_ context.Context, s string) error { replies = append(replies, s); return nil },
		ReactFunc: func(context.Context, string) error { return nil },
	}
	if err := h.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	return replies
}

func TestAsksUntilWhenThenBooksEverySlot(t *testing.T) {
	h, site, _, _ := setup(t, time.Date(2026, 11, 22, 10, 0, 0, 0, sp))

	if r := say(t, h, "unha sábado 15h"); len(r) != 1 || !strings.Contains(r[0], "até que horas") {
		t.Fatalf("expected the end-time question, got %q", r)
	}
	r := say(t, h, "18h")
	if strings.Join(site.booked, ",") != "792,793" {
		t.Fatalf("booked %v, want 792,793", site.booked)
	}
	if len(r) != 1 || !strings.Contains(r[0], "✅") || !strings.Contains(r[0], "sáb 28/11, 14h–18h") {
		t.Fatalf("reply %q", r)
	}
}

func TestShowsTheModelWhatWasAsked(t *testing.T) {
	h, _, _, _ := setup(t, time.Date(2026, 11, 22, 10, 0, 0, 0, sp))
	say(t, h, "unha sábado 15h")
	say(t, h, "18h")
	if q := h.llm.(*fakeLLM).questions; len(q) != 2 || q[1] != "Das 15h até que horas?" {
		t.Fatalf("questions passed to the model: %q", q)
	}
}

func TestNevermindDropsTheRequest(t *testing.T) {
	h, site, _, _ := setup(t, time.Date(2026, 11, 22, 10, 0, 0, 0, sp))
	say(t, h, "unha sábado 15h")
	if r := say(t, h, "esquece"); len(r) != 1 || r[0] != "Ok, deixei pra lá." {
		t.Fatalf("reply %q", r)
	}
	if r := say(t, h, "18h"); len(r) != 0 || len(site.booked) != 0 {
		t.Fatalf("still booking after nevermind: %q %v", r, site.booked)
	}
}

func TestSlotsCoverTheWholeRange(t *testing.T) {
	cases := []struct {
		start, end int
		want       string
	}{
		{14, 18, "792,793"},
		{15, 18, "792,793"}, // 15h falls in the 14–16 slot
		{15, 16, "792"},
		{6, 22, "788,789,790,791,792,793,794,795"},
	}
	for _, tc := range cases {
		var ids []string
		for _, s := range slotsFor(tc.start, tc.end) {
			ids = append(ids, s.area.ID)
		}
		if got := strings.Join(ids, ","); got != tc.want {
			t.Fatalf("%d-%d: got %s, want %s", tc.start, tc.end, got, tc.want)
		}
	}
}

func TestIgnoresChatter(t *testing.T) {
	h, site, _, _ := setup(t, time.Date(2026, 11, 22, 10, 0, 0, 0, sp))
	if r := say(t, h, "bom dia, alguém viu a chave?"); len(r) != 0 || len(site.booked) != 0 {
		t.Fatalf("replied to chatter: %q", r)
	}
}

func TestKeepsWhatItGotAndSaysWhatItMissed(t *testing.T) {
	h, site, _, _ := setup(t, time.Date(2026, 11, 22, 10, 0, 0, 0, sp))
	site.refuse["793"] = "Horário já reservado."
	r := say(t, h, "unha 05/12 14h às 18h")
	if strings.Join(site.booked, ",") != "792" {
		t.Fatalf("booked %v", site.booked)
	}
	if len(r) != 1 || !strings.Contains(r[0], "⚠️") || !strings.Contains(r[0], "14h–16h") ||
		!strings.Contains(r[0], "Não consegui 16h–18h: Horário já reservado.") {
		t.Fatalf("reply %q", r)
	}
}

func TestRejectsHoursOutsideTheChairsDay(t *testing.T) {
	h, site, _, _ := setup(t, time.Date(2026, 11, 22, 10, 0, 0, 0, sp))
	if r := say(t, h, "unha amanhã 21h às 23h"); len(r) != 1 || !strings.Contains(r[0], "6h às 22h") || len(site.booked) != 0 {
		t.Fatalf("reply %q, booked %v", r, site.booked)
	}
}

func TestSavesAFutureDateAndBooksItWhenItOpens(t *testing.T) {
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, sp)
	h, site, out, clk := setup(t, now)
	day := time.Date(2027, 1, 9, 0, 0, 0, 0, sp) // opens 2026-12-10 00:00
	site.opensAt = time.Date(2026, 12, 10, 0, 0, 3, 0, sp)

	r := say(t, h, "unha 09/01 9h às 12h")
	if len(r) != 1 || !strings.Contains(r[0], "Anotado") || !strings.Contains(r[0], "qui 10/12") {
		t.Fatalf("reply %q", r)
	}
	if r := say(t, h, "o que tem agendado?"); !strings.Contains(r[0], "sáb 09/01, 9h–12h") {
		t.Fatalf("list %q", r)
	}

	ctx := context.Background()
	h.tick(ctx) // days before: nothing happens
	if len(site.attempts) != 0 {
		t.Fatal("booked too early")
	}

	// 23:59:40: starts trying; the site opens at 00:00:03.
	clk.set(time.Date(2026, 12, 9, 23, 59, 40, 0, sp))
	h.tick(ctx)
	go func() {
		time.Sleep(20 * time.Millisecond)
		clk.set(site.opensAt)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.all(), "Reservado") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if strings.Join(site.booked, ",") != "789,790" { // 8–10 and 10–12 cover 9h–12h
		t.Fatalf("booked %v", site.booked)
	}
	if !strings.Contains(out.all(), "✅ Reservado: manicure sáb 09/01, 8h–12h.") {
		t.Fatalf("sent %q", out.all())
	}
	if site.attempts["789"] < 2 {
		t.Fatalf("expected retries before the date opened, got %d", site.attempts["789"])
	}
	time.Sleep(20 * time.Millisecond) // let fire() delete the request
	if reqs, _ := h.store.List(ctx, ""); len(reqs) != 0 {
		t.Fatalf("request not removed: %v", reqs)
	}
	_ = day
}

func TestWarnsWhenTheSessionIsDeadBeforeADateOpens(t *testing.T) {
	h, site, out, clk := setup(t, time.Date(2026, 11, 22, 10, 0, 0, 0, sp))
	say(t, h, "unha 09/01 9h às 12h")
	site.ready = superlogica.ErrSessionExpired

	clk.set(time.Date(2026, 12, 9, 23, 40, 0, 0, sp)) // 20 min before it opens
	h.tick(context.Background())
	if !strings.Contains(out.all(), "login no condomínio expirou") {
		t.Fatalf("sent %q", out.all())
	}
	h.tick(context.Background())
	if strings.Count(out.all(), "⚠️") != 1 {
		t.Fatalf("warned more than once: %q", out.all())
	}
}
