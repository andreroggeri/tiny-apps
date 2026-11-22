package reservas

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	sp, _ := time.LoadLocation("America/Sao_Paulo")
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, sp) // a Sunday
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, sp) }

	cases := []struct {
		text       string
		chair      bool
		date       time.Time
		start, end int
	}{
		{"unha sabado 15h", true, day(11, 28), 15, 0},
		{"Unha sábado 15h às 18h", true, day(11, 28), 15, 18},
		{"manicure dia 5/12 das 14 as 16h", true, day(12, 5), 14, 16},
		{"unha 05/12 14h-16h", true, day(12, 5), 14, 16},
		{"unha dia 5 as 15h", true, day(12, 5), 15, 0},
		{"unha amanhã 8h até 10h", true, day(11, 23), 8, 10},
		{"unha domingo 10h", true, day(11, 29), 10, 0}, // today is Sunday: next week
		{"cadeira 20/12 até 17h30", true, day(12, 20), 0, 18},
		{"unha 10/01 9h", true, time.Date(2027, 1, 10, 0, 0, 0, 0, sp), 9, 0},
		{"18h", false, time.Time{}, 18, 0},
		{"até as 18h", false, time.Time{}, 0, 18},
		{"bom dia", false, time.Time{}, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			p := parse(tc.text, now)
			if p.mentionsChair != tc.chair || !p.date.Equal(tc.date) || p.start != tc.start || p.end != tc.end {
				t.Fatalf("got chair=%v date=%s %d-%d, want chair=%v date=%s %d-%d",
					p.mentionsChair, p.date.Format("2006-01-02"), p.start, p.end,
					tc.chair, tc.date.Format("2006-01-02"), tc.start, tc.end)
			}
		})
	}
	if p := parse("18", now); p.bareHour != 18 {
		t.Fatalf("bare hour: got %d", p.bareHour)
	}
}

func TestSlotsCoverTheWholeRange(t *testing.T) {
	cases := []struct {
		start, end int
		want       []string
	}{
		{14, 18, []string{"792", "793"}},
		{15, 18, []string{"792", "793"}}, // 15h falls in the 14–16 slot
		{15, 16, []string{"792"}},
		{6, 22, []string{"788", "789", "790", "791", "792", "793", "794", "795"}},
	}
	for _, tc := range cases {
		got := slotsFor(tc.start, tc.end)
		var ids []string
		for _, s := range got {
			ids = append(ids, s.area.ID)
		}
		if len(ids) != len(tc.want) {
			t.Fatalf("%d-%d: got %v, want %v", tc.start, tc.end, ids, tc.want)
		}
		for i := range ids {
			if ids[i] != tc.want[i] {
				t.Fatalf("%d-%d: got %v, want %v", tc.start, tc.end, ids, tc.want)
			}
		}
	}
}
