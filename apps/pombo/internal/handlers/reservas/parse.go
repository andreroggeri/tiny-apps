package reservas

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// parsed is what a message says about a booking. Zero values mean "not said".
type parsed struct {
	mentionsChair bool
	date          time.Time // midnight in the site's time zone
	start, end    int       // hours, 0 = not said
	bareHour      int       // a lone number ("18"), only meaningful as an answer
}

var (
	reChair     = regexp.MustCompile(`\b(unha|unhas|manicure|cadeira)\b`)
	reDayMonth  = regexp.MustCompile(`\b(\d{1,2})/(\d{1,2})(?:/(\d{2,4}))?\b`)
	reDayOnly   = regexp.MustCompile(`\bdia (\d{1,2})\b`)
	reRange     = regexp.MustCompile(`\b(\d{1,2})(?:h(\d{2})?|:(\d{2}))?\s*(?:-|a|as|ate)\s*(\d{1,2})(?:h(\d{2})?|:(\d{2}))?\b`)
	reUntil     = regexp.MustCompile(`\bate (?:as )?(\d{1,2})(?:h(\d{2})?|:(\d{2}))?\b`)
	reTime      = regexp.MustCompile(`\b(\d{1,2})(?:h(\d{2})?|:(\d{2}))`)
	reBareHour  = regexp.MustCompile(`^\D*(\d{1,2})\D*$`)
	weekdays    = map[string]time.Weekday{"domingo": time.Sunday, "segunda": time.Monday, "terca": time.Tuesday, "quarta": time.Wednesday, "quinta": time.Thursday, "sexta": time.Friday, "sabado": time.Saturday}
	reWeekday   = regexp.MustCompile(`\b(domingo|segunda|terca|quarta|quinta|sexta|sabado)\b`)
	reTomorrow2 = regexp.MustCompile(`\bdepois de amanha\b`)
	reTomorrow  = regexp.MustCompile(`\bamanha\b`)
	reToday     = regexp.MustCompile(`\bhoje\b`)
)

// parse reads a message like "unha sábado 15h às 18h" or "dia 5/12 das 14 as 16h".
// now must be in the site's time zone.
func parse(text string, now time.Time) parsed {
	s := simplify(text)
	var p parsed
	p.mentionsChair = reChair.MatchString(s)

	today := midnight(now)
	// Dates first, then cut them out so "5/12" or "dia 5" isn't read as a time.
	switch {
	case reDayMonth.MatchString(s):
		m := reDayMonth.FindStringSubmatch(s)
		day, _ := strconv.Atoi(m[1])
		month, _ := strconv.Atoi(m[2])
		year := today.Year()
		if m[3] != "" {
			year, _ = strconv.Atoi(m[3])
			if year < 100 {
				year += 2000
			}
		}
		d := time.Date(year, time.Month(month), day, 0, 0, 0, 0, now.Location())
		if d.Day() == day && int(d.Month()) == month { // skip 31/02 and the like
			if m[3] == "" && d.Before(today) {
				d = d.AddDate(1, 0, 0)
			}
			p.date = d
		}
		s = reDayMonth.ReplaceAllString(s, " ")
	case reDayOnly.MatchString(s):
		day, _ := strconv.Atoi(reDayOnly.FindStringSubmatch(s)[1])
		d := time.Date(today.Year(), today.Month(), day, 0, 0, 0, 0, now.Location())
		if d.Day() == day {
			if d.Before(today) {
				d = time.Date(today.Year(), today.Month()+1, day, 0, 0, 0, 0, now.Location())
			}
			if d.Day() == day {
				p.date = d
			}
		}
		s = reDayOnly.ReplaceAllString(s, " ")
	case reTomorrow2.MatchString(s):
		p.date = today.AddDate(0, 0, 2)
	case reTomorrow.MatchString(s):
		p.date = today.AddDate(0, 0, 1)
	case reToday.MatchString(s):
		p.date = today
	case reWeekday.MatchString(s):
		want := weekdays[reWeekday.FindStringSubmatch(s)[1]]
		// Always the next one: on a Saturday, "sábado" means next week.
		ahead := (int(want) - int(today.Weekday()) + 7) % 7
		if ahead == 0 {
			ahead = 7
		}
		p.date = today.AddDate(0, 0, ahead)
	}

	switch {
	case reRange.MatchString(s) && rangeHasTimeMark(reRange.FindStringSubmatch(s)):
		m := reRange.FindStringSubmatch(s)
		p.start = startHour(m[1])
		p.end = endHour(m[4], m[5]+m[6])
	case reUntil.MatchString(s):
		m := reUntil.FindStringSubmatch(s)
		p.end = endHour(m[1], m[2]+m[3])
		if t := reTime.FindStringSubmatch(reUntil.ReplaceAllString(s, " ")); t != nil {
			p.start = startHour(t[1])
		}
	case reTime.MatchString(s):
		p.start = startHour(reTime.FindStringSubmatch(s)[1])
	}
	if m := reBareHour.FindStringSubmatch(s); m != nil {
		p.bareHour, _ = strconv.Atoi(m[1])
	}
	return p
}

// rangeHasTimeMark rejects plain number pairs ("2 a 3") unless one side
// says it's a time ("15 às 18h", "15h-18").
func rangeHasTimeMark(m []string) bool {
	return strings.Contains(m[0], "h") || strings.Contains(m[0], ":")
}

func startHour(h string) int {
	n, _ := strconv.Atoi(h)
	return n
}

// endHour rounds up: "até 17h30" needs the chair until 18h.
func endHour(h, minutes string) int {
	n, _ := strconv.Atoi(h)
	if mm, _ := strconv.Atoi(minutes); mm > 0 {
		n++
	}
	return n
}

func midnight(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// simplify lowercases s and drops accents: "Sábado às 15h" -> "sabado as 15h".
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
