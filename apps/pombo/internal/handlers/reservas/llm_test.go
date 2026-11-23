package reservas

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLLMSendsContextAndReadsTheAnswer(t *testing.T) {
	var prompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		prompt = req.Messages[0].Content
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"related\":true,\"new_request\":false,\"cancel\":false,\"list\":false,\"relative\":\"\",\"weekday\":\"\",\"day\":0,\"month\":0,\"start_hour\":0,\"end_hour\":18}"}}]}`))
	}))
	defer srv.Close()

	now := time.Date(2026, 11, 22, 10, 0, 0, 0, sp)
	d := &draft{day: day(11, 28), start: 15}
	p, err := NewLLM(srv.URL, "m").interpret(context.Background(), now, d, "Das 15h até que horas?", "18h")
	if err != nil {
		t.Fatal(err)
	}
	if !p.related || p.end != 18 || p.start != 0 || !p.date.IsZero() {
		t.Fatalf("got %+v", p)
	}
	for _, want := range []string{"domingo, 2026-11-22", "dia 2026-11-28, início 15h, fim não informado", `"Das 15h até que horas?"`, `"18h"`} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
}

func TestResolveDay(t *testing.T) {
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, sp) // a Sunday
	cases := []struct {
		name           string
		relative, wday string
		dayN, monthN   int
		want           time.Time
	}{
		{"dia 5 already past", "", "", 5, 0, day(12, 5)},
		{"dia 25", "", "", 25, 0, day(11, 25)},
		{"10/01 next year", "", "", 10, 1, time.Date(2027, 1, 10, 0, 0, 0, 0, sp)},
		{"05/12", "", "", 5, 12, day(12, 5)},
		{"31/02", "", "", 31, 2, time.Time{}},
		{"domingo on a Sunday", "", "domingo", 0, 0, day(11, 29)},
		{"sabado", "", "sabado", 0, 0, day(11, 28)},
		{"amanha", "amanha", "", 0, 0, day(11, 23)},
		{"nothing", "", "", 0, 0, time.Time{}},
	}
	for _, tc := range cases {
		if got := resolveDay(now, tc.relative, tc.wday, tc.dayN, tc.monthN); !got.Equal(tc.want) {
			t.Errorf("%s: got %s, want %s", tc.name, got.Format(time.DateOnly), tc.want.Format(time.DateOnly))
		}
	}
}
