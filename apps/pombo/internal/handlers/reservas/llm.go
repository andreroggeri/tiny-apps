package reservas

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// LLM reads booking messages with an OpenAI-compatible chat API (the same
// local model the documents handler uses). Without it, or when it fails,
// the handler falls back to parse.go's patterns.
type LLM struct {
	url   string
	model string
	http  *http.Client
}

func NewLLM(baseURL, model string) *LLM {
	return &LLM{url: strings.TrimRight(baseURL, "/") + "/chat/completions", model: model, http: &http.Client{Timeout: 60 * time.Second}}
}

var weekdayLong = [...]string{"domingo", "segunda-feira", "terça-feira", "quarta-feira", "quinta-feira", "sexta-feira", "sábado"}

const llmPrompt = `Você lê mensagens de um grupo de WhatsApp da família e ajuda a reservar a cadeira de manicure do condomínio.
A cadeira funciona das 6h às 22h. Hoje é %s, %s.
%s
Mensagem: %q

Responda em JSON:
- "related": a mensagem é sobre reservar a cadeira (pedido novo, resposta à pergunta do pedido em andamento, desistência ou pedido pra ver as reservas)? Conversa comum = false.
- "new_request": a mensagem começa um pedido novo de reserva (cita unha, manicure ou a cadeira).
- "cancel": a pessoa desistiu do pedido em andamento.
- "list": a pessoa quer ver as reservas agendadas.
- O dia, exatamente como a mensagem diz (não calcule datas; deixe vazio/0 o que ela não diz):
  - "relative": "hoje", "amanha" ou "depois de amanha", senão "".
  - "weekday": "domingo", "segunda", "terca", "quarta", "quinta", "sexta" ou "sabado", senão "".
  - "day" e "month": o número do dia e do mês, se ditos ("dia 5" = day 5, month 0; "10/01" = day 10, month 1).
- "start_hour" e "end_hour": horas inteiras, ou 0 se a mensagem não diz. Início arredondado pra baixo, fim pra cima ("até 17h30" = 18). Use o pedido em andamento pra entender respostas curtas: "18h" respondendo "até que horas?" é o end_hour. "3 da tarde" = 15.
Só preencha o que a mensagem diz; não repita dados do pedido em andamento.`

// interpret asks the model what the message says. d is the request in
// progress for this sender (nil if none) and question what the bot last asked.
func (l *LLM) interpret(ctx context.Context, now time.Time, d *draft, question, text string) (parsed, error) {
	inProgress := "Não há pedido em andamento."
	if d != nil {
		day := "não informado"
		if !d.day.IsZero() {
			day = d.day.Format(time.DateOnly)
		}
		inProgress = fmt.Sprintf("Pedido em andamento: dia %s, início %s, fim %s. O bot perguntou: %q",
			day, hourOrUnknown(d.start), hourOrUnknown(d.end), question)
	}
	prompt := fmt.Sprintf(llmPrompt, weekdayLong[now.Weekday()], now.Format("2006-01-02"), inProgress, text)

	body, _ := json.Marshal(map[string]any{
		"model":                l.model,
		"messages":             []map[string]any{{"role": "user", "content": prompt}},
		"temperature":          0,
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": "booking",
				"schema": map[string]any{
					"type":     "object",
					"required": []string{"related", "new_request", "cancel", "list", "relative", "weekday", "day", "month", "start_hour", "end_hour"},
					"properties": map[string]any{
						"related":     map[string]any{"type": "boolean"},
						"new_request": map[string]any{"type": "boolean"},
						"cancel":      map[string]any{"type": "boolean"},
						"list":        map[string]any{"type": "boolean"},
						"relative":    map[string]any{"type": "string", "enum": []string{"", "hoje", "amanha", "depois de amanha"}},
						"weekday":     map[string]any{"type": "string", "enum": []string{"", "domingo", "segunda", "terca", "quarta", "quinta", "sexta", "sabado"}},
						"day":         map[string]any{"type": "integer"},
						"month":       map[string]any{"type": "integer"},
						"start_hour":  map[string]any{"type": "integer"},
						"end_hour":    map[string]any{"type": "integer"},
					},
				},
			},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.url, bytes.NewReader(body))
	if err != nil {
		return parsed{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := l.http.Do(req)
	if err != nil {
		return parsed{}, fmt.Errorf("llm: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return parsed{}, fmt.Errorf("llm: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return parsed{}, fmt.Errorf("llm: %w", err)
	}
	if len(out.Choices) == 0 {
		return parsed{}, fmt.Errorf("llm: empty response")
	}
	var a struct {
		Related    bool   `json:"related"`
		NewRequest bool   `json:"new_request"`
		Cancel     bool   `json:"cancel"`
		List       bool   `json:"list"`
		Relative   string `json:"relative"`
		Weekday    string `json:"weekday"`
		Day        int    `json:"day"`
		Month      int    `json:"month"`
		Start      int    `json:"start_hour"`
		End        int    `json:"end_hour"`
	}
	if err := json.Unmarshal([]byte(out.Choices[0].Message.Content), &a); err != nil {
		return parsed{}, fmt.Errorf("llm: answer is not the JSON asked for: %w", err)
	}
	p := parsed{related: a.Related, mentionsChair: a.NewRequest, cancel: a.Cancel, list: a.List, start: a.Start, end: a.End}
	p.date = resolveDay(now, a.Relative, a.Weekday, a.Day, a.Month)
	return p, nil
}

var weekdayNames = map[string]time.Weekday{"domingo": time.Sunday, "segunda": time.Monday, "terca": time.Tuesday,
	"quarta": time.Wednesday, "quinta": time.Thursday, "sexta": time.Friday, "sabado": time.Saturday}

// resolveDay turns what the message said into a date. The model only
// extracts the words; it gets calendar math wrong, so that's done here.
// Returns the zero time when nothing (valid) was said.
func resolveDay(now time.Time, relative, weekday string, day, month int) time.Time {
	today := midnight(now)
	switch {
	case day > 0 && month > 0:
		d := time.Date(today.Year(), time.Month(month), day, 0, 0, 0, 0, now.Location())
		if d.Day() != day {
			return time.Time{} // 31/02 and the like
		}
		if d.Before(today) {
			d = d.AddDate(1, 0, 0)
		}
		return d
	case day > 0: // "dia 5": this month, or next if it's past
		for i := 0; i < 2; i++ {
			d := time.Date(today.Year(), today.Month()+time.Month(i), day, 0, 0, 0, 0, now.Location())
			if d.Day() == day && !d.Before(today) {
				return d
			}
		}
		return time.Time{}
	case relative == "hoje":
		return today
	case relative == "amanha":
		return today.AddDate(0, 0, 1)
	case relative == "depois de amanha":
		return today.AddDate(0, 0, 2)
	case weekday != "":
		want, ok := weekdayNames[weekday]
		if !ok {
			return time.Time{}
		}
		// Always the next one: on a Sunday, "domingo" means next week.
		ahead := (int(want) - int(today.Weekday()) + 7) % 7
		if ahead == 0 {
			ahead = 7
		}
		return today.AddDate(0, 0, ahead)
	}
	return time.Time{}
}

func hourOrUnknown(h int) string {
	if h == 0 {
		return "não informado"
	}
	return fmt.Sprintf("%dh", h)
}
