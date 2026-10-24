// Package grouping asks a vision LLM which photos in a batch are pages of the
// same document.
package grouping

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
)

type Page struct {
	Image    []byte
	MimeType string
	Caption  string // context the sender wrote with this photo, if any
}

// LLM groups pages using an OpenAI-compatible chat API with image input
// (e.g. llama.cpp serving Gemma).
type LLM struct {
	url   string
	model string
	http  *http.Client
}

func NewLLM(baseURL, model string) *LLM {
	return &LLM{url: strings.TrimRight(baseURL, "/") + "/chat/completions", model: model, http: &http.Client{Timeout: 3 * time.Minute}}
}

const prompt = `Você recebeu %d fotos enviadas em sequência por uma pessoa para arquivar.
Algumas podem ser páginas do mesmo documento (ex.: frente e verso, várias páginas de um contrato);
outras podem ser documentos diferentes (ex.: duas contas distintas).
%s
Agrupe as fotos por documento, mantendo a ordem das páginas. Use os números das fotos (1 a %d).
Cada foto deve aparecer em exatamente um grupo.`

// Group returns groups of page indexes (0-based). Each index appears exactly
// once; anything else from the model is rejected as an error.
func (l *LLM) Group(ctx context.Context, pages []Page, batchContext string) ([][]int, error) {
	if len(pages) < 2 {
		return [][]int{{0}}, nil
	}

	var hints strings.Builder
	if batchContext != "" {
		fmt.Fprintf(&hints, "Contexto escrito pela pessoa: %q\n", batchContext)
	}
	content := []map[string]any{}
	for i, p := range pages {
		label := fmt.Sprintf("Foto %d:", i+1)
		if p.Caption != "" {
			label += fmt.Sprintf(" (legenda: %q)", p.Caption)
		}
		content = append(content,
			map[string]any{"type": "text", "text": label},
			map[string]any{"type": "image_url", "image_url": map[string]string{
				"url": "data:" + p.MimeType + ";base64," + base64.StdEncoding.EncodeToString(p.Image),
			}},
		)
	}
	content = append(content, map[string]any{"type": "text", "text": fmt.Sprintf(prompt, len(pages), hints.String(), len(pages))})

	body, _ := json.Marshal(map[string]any{
		"model":    l.model,
		"messages": []map[string]any{{"role": "user", "content": content}},
		// A quick visual judgement; thinking would only add latency.
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": "groups",
				"schema": map[string]any{
					"type":     "object",
					"required": []string{"documents"},
					"properties": map[string]any{
						"documents": map[string]any{
							"type":  "array",
							"items": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
						},
					},
				},
			},
		},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := l.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("llm: %s: %s", resp.Status, bytes.TrimSpace(msg))
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("llm: empty response")
	}
	var parsed struct {
		Documents [][]int `json:"documents"`
	}
	if err := json.Unmarshal([]byte(out.Choices[0].Message.Content), &parsed); err != nil {
		return nil, fmt.Errorf("llm: invalid JSON %q: %w", out.Choices[0].Message.Content, err)
	}
	return toZeroBased(parsed.Documents, len(pages))
}

// toZeroBased converts the model's 1-based groups and checks they cover every
// page exactly once.
func toZeroBased(groups [][]int, n int) ([][]int, error) {
	seen := make([]bool, n)
	out := make([][]int, 0, len(groups))
	for _, g := range groups {
		if len(g) == 0 {
			continue
		}
		zg := make([]int, len(g))
		for i, p := range g {
			if p < 1 || p > n || seen[p-1] {
				return nil, fmt.Errorf("llm: invalid grouping %v for %d photos", groups, n)
			}
			seen[p-1] = true
			zg[i] = p - 1
		}
		out = append(out, zg)
	}
	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("llm: grouping %v leaves out photo %d", groups, i+1)
		}
	}
	return out, nil
}
