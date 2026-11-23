// Package reservas books the condo's manicure chair from a WhatsApp message,
// like "unha sábado 15h às 18h".
//
// The chair is booked in 2-hour slots (6h to 22h), so the whole range asked
// for is covered: 15h–18h takes the 14–16 and 16–18 slots. If the message
// leaves out the day or the hours, the bot asks. Dates open for booking 30
// days ahead at midnight; a request for a later date is saved and booked the
// moment it opens. Slots that couldn't be booked are reported; the ones that
// were booked are kept.
package reservas

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/router"
	"github.com/andreroggeri/tiny-apps/apps/pombo/internal/superlogica"
)

// Name is the handler name shown in the dashboard.
const Name = "reservas"

const (
	// windowDays is how far ahead a date opens: Dec 22 opens on Nov 22, at 00:00.
	windowDays = 30
	firstHour  = 6
	lastHour   = 22
	slotHours  = 2
	// draftTTL is how long the bot waits for an answer to its question.
	draftTTL = 15 * time.Minute
	// sessionCheckBefore is how long before a date opens the session is
	// checked, so there's time to log in again.
	sessionCheckBefore = 30 * time.Minute
)

// chairAreas are the site's area IDs for "BE CADEIRA 01 MANICURE", one per
// 2-hour slot starting at 6h.
var chairAreas = []superlogica.Area{
	{ID: "788", Name: "BE  CADEIRA 01 MANICURE   - 06h às 08h"},
	{ID: "789", Name: "BE  CADEIRA 01 MANICURE   - 08h às 10h"},
	{ID: "790", Name: "BE  CADEIRA 01 MANICURE   - 10h às 12h"},
	{ID: "791", Name: "BE  CADEIRA 01 MANICURE   - 12h às 14h"},
	{ID: "792", Name: "BE  CADEIRA 01 MANICURE   - 14h às 16h"},
	{ID: "793", Name: "BE  CADEIRA 01 MANICURE   - 16h às 18h"},
	{ID: "794", Name: "BE  CADEIRA 01 MANICURE   - 18h às 20h"},
	{ID: "795", Name: "BE  CADEIRA 01 MANICURE   - 20h às 22h"},
}

type slot struct {
	start int
	area  superlogica.Area
}

// slotsFor returns the slots covering start..end.
func slotsFor(start, end int) []slot {
	var out []slot
	for i, a := range chairAreas {
		s := firstHour + i*slotHours
		if s < end && s+slotHours > start {
			out = append(out, slot{start: s, area: a})
		}
	}
	return out
}

// Site is the condo website.
type Site interface {
	Ready(ctx context.Context) error // a working session, logging in if needed
	Book(ctx context.Context, area superlogica.Area, day time.Time) (superlogica.Result, error)
}

// Messenger posts to a group outside of a reply, for bookings made later.
type Messenger interface {
	Send(ctx context.Context, chatJID, text string) error
}

type draftKey struct{ chat, sender string }

type draft struct {
	day        time.Time
	start, end int
	question   string // what the bot last asked, so the LLM can read short answers
	expires    time.Time
}

// interpreter reads a message; *LLM is the real one.
type interpreter interface {
	interpret(ctx context.Context, now time.Time, d *draft, question, text string) (parsed, error)
}

type Handler struct {
	site  Site
	store *Store
	msg   Messenger
	llm   interpreter
	log   *slog.Logger
	loc   *time.Location
	now   func() time.Time

	// How often a not-yet-open slot is retried, and for how long after the
	// date opens.
	pollEvery time.Duration
	pollFor   time.Duration

	mu      sync.Mutex
	drafts  map[draftKey]*draft
	checked map[int64]bool // pending requests whose session was checked
	running map[int64]bool // pending requests being booked now
}

func New(site Site, store *Store, msg Messenger, llm *LLM, loc *time.Location, log *slog.Logger) *Handler {
	return &Handler{
		site: site, store: store, msg: msg, llm: llm, log: log, loc: loc, now: time.Now,
		pollEvery: 2 * time.Second, pollFor: 10 * time.Minute,
		drafts: map[draftKey]*draft{}, checked: map[int64]bool{}, running: map[int64]bool{},
	}
}

func (h *Handler) Handle(ctx context.Context, m router.Message) error {
	text := strings.TrimSpace(m.Text)
	if text == "" {
		return nil
	}
	now := h.now().In(h.loc)
	key := draftKey{m.ChatJID, m.SenderPhone}

	var current *draft
	h.mu.Lock()
	if d := h.drafts[key]; d != nil && now.After(d.expires) {
		delete(h.drafts, key)
	} else if d != nil {
		c := *d
		current = &c
	}
	h.mu.Unlock()
	question := ""
	if current != nil {
		question = current.question
	}

	p, err := h.llm.interpret(ctx, now, current, question, text)
	if err != nil {
		return fmt.Errorf("não consegui ler a mensagem: %w", err)
	}
	switch {
	case !p.related:
		return nil // chatter, not for us
	case p.list:
		return h.listPending(ctx, m)
	case p.cancel:
		h.mu.Lock()
		delete(h.drafts, key)
		h.mu.Unlock()
		if current == nil {
			return nil
		}
		return m.Reply(ctx, "Ok, deixei pra lá.")
	}

	h.mu.Lock()
	d := h.drafts[key]
	switch {
	case p.mentionsChair:
		d = &draft{day: p.date, start: p.start, end: p.end}
	case d == nil:
		h.mu.Unlock()
		return nil // an answer to a question that expired
	default:
		d.merge(p)
	}
	d.expires = now.Add(draftTTL)
	d.question = ""
	switch {
	case d.day.IsZero():
		d.question = "Pra qual dia? (ex.: sábado, 05/12)"
	case d.start == 0:
		d.question = "A partir de que horas?"
	case d.end == 0:
		d.question = fmt.Sprintf("Das %dh até que horas?", d.start)
	}
	if d.question != "" {
		h.drafts[key] = d
		h.mu.Unlock()
		return m.Reply(ctx, d.question)
	}
	delete(h.drafts, key)
	h.mu.Unlock()

	req := request{Chat: m.ChatJID, Sender: m.SenderPhone, Day: d.day, Start: d.start, End: d.end}
	if problem := h.validate(req, now); problem != "" {
		return m.Reply(ctx, problem)
	}
	return h.request(ctx, m, req, now)
}

// merge fills in what an answer adds.
func (d *draft) merge(p parsed) {
	if !p.date.IsZero() {
		d.day = p.date
	}
	if p.start != 0 {
		d.start = p.start
	}
	if p.end != 0 {
		d.end = p.end
	}
}

func (h *Handler) validate(r request, now time.Time) string {
	if r.Start < firstHour || r.End > lastHour || r.Start >= r.End {
		return fmt.Sprintf("A cadeira funciona das %dh às %dh. Me manda de novo com outro horário.", firstHour, lastHour)
	}
	if !r.Day.After(midnight(now)) {
		return "Só dá pra reservar a partir de amanhã."
	}
	return ""
}

func openAt(day time.Time) time.Time { return day.AddDate(0, 0, -windowDays) }

func (h *Handler) request(ctx context.Context, m router.Message, r request, now time.Time) error {
	if now.Before(openAt(r.Day)) {
		id, err := h.store.Add(ctx, r)
		if err != nil {
			return err
		}
		h.log.Info("booking saved for when the date opens", "id", id, "day", r.Day.Format(time.DateOnly), "opens", openAt(r.Day))
		return m.Reply(ctx, fmt.Sprintf("📅 Anotado: manicure %s. As reservas desse dia abrem %s à meia-noite; reservo na hora e aviso aqui.",
			describe(r.Day, r.Start, r.End), dayLabel(openAt(r.Day))))
	}
	_ = m.React(ctx, "⏳")
	// Open already: one try per slot, no polling.
	results := h.book(ctx, r, now)
	text, emoji := report(r, results)
	_ = m.React(ctx, emoji)
	return m.Reply(ctx, text)
}

type slotResult struct {
	slot
	ok     bool
	queue  string // waiting-list position when booked; "1" or "" = first
	reason string // why not, when !ok
}

// book tries every slot until each is booked or refused, retrying slots that
// aren't open yet until deadline.
func (h *Handler) book(ctx context.Context, r request, deadline time.Time) []slotResult {
	slots := slotsFor(r.Start, r.End)
	results := make([]slotResult, len(slots))
	for i, s := range slots {
		results[i] = slotResult{slot: s, reason: "não tentei"}
	}
	if err := h.site.Ready(ctx); err != nil {
		h.log.Warn("no superlogica session", "err", err)
		for i := range results {
			results[i].reason = loginReason(err)
		}
		return results
	}

	todo := make([]int, len(slots))
	for i := range todo {
		todo[i] = i
	}
	for {
		var again []int
		for _, i := range todo {
			res, err := h.site.Book(ctx, slots[i].area, r.Day)
			switch {
			case errors.Is(err, superlogica.ErrBlocked):
				h.log.Error("superlogica blocked us; stopping", "err", err)
				for _, j := range todo {
					results[j].reason = "o site bloqueou as tentativas"
				}
				return results
			case errors.Is(err, superlogica.ErrSessionExpired):
				if h.site.Ready(ctx) != nil {
					results[i].reason = loginReason(err)
					continue
				}
				again = append(again, i)
			case err != nil:
				h.log.Warn("booking attempt failed", "area", slots[i].area.ID, "err", err)
				results[i].reason = "erro de conexão com o site"
				again = append(again, i)
			case res.Outcome == superlogica.Booked:
				results[i].ok, results[i].queue = true, res.Queue
				h.log.Info("booked", "area", slots[i].area.ID, "day", r.Day.Format(time.DateOnly), "id", res.ID, "queue", res.Queue)
			case res.Outcome == superlogica.AlreadyOurs:
				results[i].ok = true
			case res.Outcome == superlogica.NotOpen:
				results[i].reason = "a data ainda não tinha aberto"
				again = append(again, i)
			default:
				results[i].reason = res.Msg
			}
		}
		todo = again
		if len(todo) == 0 || !h.now().Before(deadline) {
			return results
		}
		select {
		case <-ctx.Done():
			return results
		case <-time.After(h.pollEvery):
		}
	}
}

func loginReason(err error) string {
	if errors.Is(err, superlogica.ErrSessionExpired) {
		return "o login no condomínio expirou"
	}
	return err.Error()
}

// report turns results into the group message and a reaction.
func report(r request, results []slotResult) (string, string) {
	var ok, failed []slotResult
	for _, res := range results {
		if res.ok {
			ok = append(ok, res)
		} else {
			failed = append(failed, res)
		}
	}
	day := dayLabel(r.Day)
	var queued []string
	for _, res := range ok {
		if res.queue != "" && res.queue != "1" {
			queued = append(queued, fmt.Sprintf("%s (posição %s)", hours(res.start, res.start+slotHours), res.queue))
		}
	}
	queueNote := ""
	if len(queued) > 0 {
		queueNote = "\nNa fila de espera: " + strings.Join(queued, ", ") + "."
	}
	switch {
	case len(failed) == 0:
		return fmt.Sprintf("✅ Reservado: manicure %s, %s.%s", day, ranges(ok), queueNote), "✅"
	case len(ok) == 0:
		return fmt.Sprintf("❌ Não consegui reservar manicure %s, %s: %s.", day, hours(r.Start, r.End), reasons(failed)), "❌"
	default:
		return fmt.Sprintf("⚠️ Reservei só parte: manicure %s, %s.\nNão consegui %s: %s.%s",
			day, ranges(ok), ranges(failed), reasons(failed), queueNote), "⚠️"
	}
}

// ranges joins consecutive slots: 14–16 and 16–18 become "14h–18h".
func ranges(rs []slotResult) string {
	var parts []string
	for i := 0; i < len(rs); {
		j := i
		for j+1 < len(rs) && rs[j+1].start == rs[j].start+slotHours {
			j++
		}
		parts = append(parts, hours(rs[i].start, rs[j].start+slotHours))
		i = j + 1
	}
	return strings.Join(parts, " e ")
}

func reasons(rs []slotResult) string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rs {
		if !seen[r.reason] {
			seen[r.reason] = true
			out = append(out, r.reason)
		}
	}
	return strings.Join(out, "; ")
}

func hours(start, end int) string { return fmt.Sprintf("%dh–%dh", start, end) }

var weekdayShort = [...]string{"dom", "seg", "ter", "qua", "qui", "sex", "sáb"}

func dayLabel(d time.Time) string {
	return weekdayShort[d.Weekday()] + " " + d.Format("02/01")
}

func describe(day time.Time, start, end int) string {
	return dayLabel(day) + ", " + hours(start, end)
}

func (h *Handler) listPending(ctx context.Context, m router.Message) error {
	reqs, err := h.store.List(ctx, m.ChatJID)
	if err != nil {
		return err
	}
	if len(reqs) == 0 {
		return m.Reply(ctx, "Nenhuma reserva esperando a data abrir.")
	}
	lines := []string{"Esperando a data abrir:"}
	for _, r := range reqs {
		lines = append(lines, fmt.Sprintf("• manicure %s (abre %s)", describe(r.Day, r.Start, r.End), dayLabel(openAt(r.Day))))
	}
	return m.Reply(ctx, strings.Join(lines, "\n"))
}

// Run books saved requests when their dates open. It also checks the
// session a while before, so there's time to log in if it expired.
func (h *Handler) Run(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		h.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (h *Handler) tick(ctx context.Context) {
	reqs, err := h.store.List(ctx, "")
	if err != nil {
		h.log.Error("list pending bookings", "err", err)
		return
	}
	now := h.now().In(h.loc)
	for _, r := range reqs {
		opens := openAt(r.Day)
		h.mu.Lock()
		check := !h.checked[r.ID] && !now.Before(opens.Add(-sessionCheckBefore))
		fire := !h.running[r.ID] && !now.Before(opens.Add(-30*time.Second))
		if check {
			h.checked[r.ID] = true
		}
		if fire {
			h.running[r.ID] = true
		}
		h.mu.Unlock()

		if check && !fire {
			if err := h.site.Ready(ctx); err != nil {
				h.log.Warn("session not ready before a booking opens", "id", r.ID, "err", err)
				h.send(ctx, r.Chat, fmt.Sprintf("⚠️ A reserva de manicure %s abre %s à meia-noite, mas %s. Alguém precisa fazer login de novo antes disso.",
					describe(r.Day, r.Start, r.End), dayLabel(opens), loginReason(err)))
			}
		}
		if fire {
			go h.fire(ctx, r)
		}
	}
}

func (h *Handler) fire(ctx context.Context, r request) {
	defer func() {
		h.mu.Lock()
		delete(h.running, r.ID)
		delete(h.checked, r.ID)
		h.mu.Unlock()
	}()
	deadline := openAt(r.Day).Add(h.pollFor)
	if now := h.now(); now.After(openAt(r.Day)) {
		deadline = now.Add(h.pollFor) // pombo was down when it opened
	}
	results := h.book(ctx, r, deadline)
	if ctx.Err() != nil {
		return // shutting down; the request stays saved and runs on restart
	}
	text, _ := report(r, results)
	h.send(ctx, r.Chat, text)
	if err := h.store.Delete(ctx, r.ID); err != nil {
		h.log.Error("delete finished booking", "id", r.ID, "err", err)
	}
}

func (h *Handler) send(ctx context.Context, chat, text string) {
	if err := h.msg.Send(ctx, chat, text); err != nil {
		h.log.Error("send to group failed", "chat", chat, "err", err)
	}
}
