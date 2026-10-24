// Package grouping asks a vision LLM which photos in a batch are pages of the
// same document, which of the sender's words describe each document, and
// whether a new photo continues a document filed a few minutes earlier.
package grouping

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Entry is one message in a batch, in the order it was sent: a photo (Image
// set, Text is its caption) or a text message (Image nil).
type Entry struct {
	Image    []byte
	MimeType string
	Text     string
}

// Recent is a document filed a few minutes ago that new photos may continue.
// Text is its OCR text; Image (its last page) is sent when the text is too
// short to tell what the document is.
type Recent struct {
	Text     string
	Image    []byte
	MimeType string
}

// Document is a group of photos, by index among the batch's photos (not
// entries), plus the sender's description of it ("" if none).
type Document struct {
	Pages   []int
	Context string
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

const prompt = `Acima estão, na ordem em que foram enviadas, %d fotos e as mensagens de texto que uma pessoa mandou para arquivar.
Algumas fotos podem ser páginas do mesmo documento (ex.: frente e verso, várias páginas de um contrato);
outras podem ser documentos diferentes (ex.: duas contas distintas).

1. Agrupe as fotos por documento, mantendo a ordem das páginas. Use os números das fotos (1 a %d).
   Cada foto deve aparecer em exatamente um grupo.
2. Para cada documento, preencha "context" com o que a pessoa escreveu sobre ele (legendas ou mensagens),
   com as palavras dela. Uma mensagem só vale para o documento ao qual se refere; use as imagens para decidir.
   Deixe vazio se nada do que ela escreveu for sobre aquele documento.`

// Group returns the batch's photos grouped into documents. Every photo
// appears in exactly one document; anything else from the model is an error.
func (l *LLM) Group(ctx context.Context, entries []Entry) ([]Document, error) {
	var content []map[string]any
	photos := 0
	for _, e := range entries {
		if e.Image == nil {
			content = append(content, text(fmt.Sprintf("Mensagem de texto: %q", e.Text)))
			continue
		}
		photos++
		label := fmt.Sprintf("Foto %d:", photos)
		if e.Text != "" {
			label += fmt.Sprintf(" (legenda: %q)", e.Text)
		}
		content = append(content, text(label), image(e.MimeType, e.Image))
	}
	if photos == 0 {
		return nil, nil
	}
	content = append(content, text(fmt.Sprintf(prompt, photos, photos)))

	reply, err := l.chat(ctx, map[string]any{
		"model":    l.model,
		"messages": []map[string]any{{"role": "user", "content": content}},
		// A quick visual judgement; thinking would only add latency.
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": "documents",
				"schema": map[string]any{
					"type":     "object",
					"required": []string{"documents"},
					"properties": map[string]any{
						"documents": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type":     "object",
								"required": []string{"pages", "context"},
								"properties": map[string]any{
									"pages":   map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
									"context": map[string]any{"type": "string"},
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Documents []struct {
			Pages   []int  `json:"pages"`
			Context string `json:"context"`
		} `json:"documents"`
	}
	if err := json.Unmarshal([]byte(reply.Content), &parsed); err != nil {
		return nil, fmt.Errorf("llm: invalid JSON %q: %w", reply.Content, err)
	}
	docs := make([]Document, 0, len(parsed.Documents))
	for _, d := range parsed.Documents {
		docs = append(docs, Document{Pages: d.Pages, Context: strings.TrimSpace(d.Context)})
	}
	return toZeroBased(docs, photos)
}

const promptSame = `The first part above is a document filed a few minutes ago. The photo after it was sent now.
Is the new photo another page of the same document (e.g. its back side, or the next page of the same contract or bill)?
Answer only yes or no.`

// SameDocument returns the model's probability that page is another page of
// r. It reads the probability of the first answer token rather than the
// answer itself, so callers can demand high confidence before acting. It's a
// separate question rather than part of Group: offered filed documents
// alongside new photos, the model mixes them up.
func (l *LLM) SameDocument(ctx context.Context, r Recent, page Entry) (float64, error) {
	content := recentContent(r)
	content = append(content, text("New photo:"), image(page.MimeType, page.Image), text(promptSame))
	reply, err := l.chat(ctx, map[string]any{
		"model":                l.model,
		"messages":             []map[string]any{{"role": "user", "content": content}},
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
		"max_tokens":           1,
		"logprobs":             true,
		"top_logprobs":         10,
	})
	if err != nil {
		return 0, err
	}
	if len(reply.Logprobs.Content) == 0 {
		return 0, fmt.Errorf("llm: no logprobs in reply")
	}
	var yes, no float64
	for _, t := range reply.Logprobs.Content[0].TopLogprobs {
		switch strings.ToLower(strings.TrimSpace(t.Token)) {
		case "yes":
			yes += math.Exp(t.Logprob)
		case "no":
			no += math.Exp(t.Logprob)
		}
	}
	if yes+no == 0 {
		return 0, fmt.Errorf("llm: answer %q is neither yes nor no", reply.Content)
	}
	return yes / (yes + no), nil
}

type reply struct {
	Content  string
	Logprobs struct {
		Content []struct {
			TopLogprobs []struct {
				Token   string  `json:"token"`
				Logprob float64 `json:"logprob"`
			} `json:"top_logprobs"`
		} `json:"content"`
	}
}

func (l *LLM) chat(ctx context.Context, request map[string]any) (reply, error) {
	body, _ := json.Marshal(request)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.url, bytes.NewReader(body))
	if err != nil {
		return reply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := l.http.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return reply{}, fmt.Errorf("llm: %s: %s", resp.Status, bytes.TrimSpace(msg))
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Logprobs json.RawMessage `json:"logprobs"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return reply{}, err
	}
	if len(out.Choices) == 0 {
		return reply{}, fmt.Errorf("llm: empty response")
	}
	r := reply{Content: out.Choices[0].Message.Content}
	if len(out.Choices[0].Logprobs) > 0 {
		if err := json.Unmarshal(out.Choices[0].Logprobs, &r.Logprobs); err != nil {
			return reply{}, fmt.Errorf("llm: logprobs: %w", err)
		}
	}
	return r, nil
}

// recentContent describes a recent document by its text, or by its last
// page when it has no text to speak of.
func recentContent(r Recent) []map[string]any {
	if r.Image != nil {
		return []map[string]any{text("Filed document (its last page):"), image(r.MimeType, r.Image)}
	}
	return []map[string]any{text("Filed document (its OCR text):\n" + r.Text)}
}

func text(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

func image(mimeType string, data []byte) map[string]any {
	return map[string]any{"type": "image_url", "image_url": map[string]string{
		"url": "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data),
	}}
}

// toZeroBased converts the model's 1-based pages and checks they cover every
// photo exactly once.
func toZeroBased(docs []Document, n int) ([]Document, error) {
	seen := make([]bool, n)
	out := make([]Document, 0, len(docs))
	for _, d := range docs {
		if len(d.Pages) == 0 {
			continue
		}
		pages := make([]int, len(d.Pages))
		for i, p := range d.Pages {
			if p < 1 || p > n || seen[p-1] {
				return nil, fmt.Errorf("llm: invalid grouping %v for %d photos", docs, n)
			}
			seen[p-1] = true
			pages[i] = p - 1
		}
		d.Pages = pages
		out = append(out, d)
	}
	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("llm: grouping leaves out photo %d", i+1)
		}
	}
	return out, nil
}
