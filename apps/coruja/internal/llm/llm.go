// Package llm asks a vision LLM for a document's title and a one-line
// summary, through an OpenAI-compatible chat API (e.g. llama.cpp serving
// Gemma).
package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// maxText is how much OCR text is sent. Titles and summaries come from the
// first pages; the rest only adds latency.
const maxText = 6000

// MaxSummary is the longest summary accepted, in characters: one line in a
// chat message.
const MaxSummary = 160

type Client struct {
	url   string
	model string
	http  *http.Client
}

func New(baseURL, model string) *Client {
	return &Client{url: strings.TrimRight(baseURL, "/") + "/chat/completions", model: model, http: &http.Client{Timeout: 3 * time.Minute}}
}

// Document is what the model sees: the OCR text and, for photos, the image
// itself (OCR of a phone photo or screenshot is often garbage).
type Document struct {
	Text     string
	Image    []byte
	MimeType string
}

type Summary struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

const prompt = `Acima está um documento arquivado no paperless de uma família brasileira.
Responda em português:
- "title": um título curto e específico, como uma pessoa daria (ex.: "Conta de luz Enel setembro 2026", "CNH do André").
  No máximo 60 caracteres.
- "summary": uma frase dizendo o que é o documento e o mais importante nele (valores, datas, vencimentos, nomes).
  No máximo 140 caracteres. Sem repetir o título.
Nunca inclua números de documentos (CPF, RG, CNH, registro, conta, código de barras, chave Pix):
o resumo é enviado num grupo de WhatsApp.`

func (c *Client) Summarize(ctx context.Context, doc Document) (Summary, error) {
	var content []map[string]any
	if doc.Image != nil {
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]string{
			"url": "data:" + doc.MimeType + ";base64," + base64.StdEncoding.EncodeToString(doc.Image),
		}})
	}
	if text := strings.TrimSpace(doc.Text); text != "" {
		if len(text) > maxText {
			text = text[:maxText]
			for !utf8.ValidString(text) {
				text = text[:len(text)-1]
			}
		}
		content = append(content, map[string]any{"type": "text", "text": "Texto extraído (OCR):\n" + text})
	}
	if content == nil {
		return Summary{}, fmt.Errorf("llm: document has no text or image")
	}
	content = append(content, map[string]any{"type": "text", "text": prompt})

	body, _ := json.Marshal(map[string]any{
		"model":    c.model,
		"messages": []map[string]any{{"role": "user", "content": content}},
		// A short description; thinking would only add latency.
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": "summary",
				"schema": map[string]any{
					"type":     "object",
					"required": []string{"title", "summary"},
					"properties": map[string]any{
						"title":   map[string]any{"type": "string"},
						"summary": map[string]any{"type": "string"},
					},
				},
			},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return Summary{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Summary{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Summary{}, fmt.Errorf("llm: %s: %s", resp.Status, bytes.TrimSpace(msg))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Summary{}, err
	}
	if len(out.Choices) == 0 {
		return Summary{}, fmt.Errorf("llm: empty response")
	}
	var s Summary
	if err := json.Unmarshal([]byte(out.Choices[0].Message.Content), &s); err != nil {
		return Summary{}, fmt.Errorf("llm: invalid JSON %q: %w", out.Choices[0].Message.Content, err)
	}
	s.Title, s.Summary = clip(s.Title, 128), clip(s.Summary, MaxSummary)
	if s.Title == "" || s.Summary == "" {
		return Summary{}, fmt.Errorf("llm: empty title or summary in %q", out.Choices[0].Message.Content)
	}
	return s, nil
}

// clip trims s to one line of at most n characters, ending in "…" when cut.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
