package grouping

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestToZeroBased(t *testing.T) {
	tests := []struct {
		name   string
		groups [][]int
		n      int
		want   [][]int
		ok     bool
	}{
		{"two documents", [][]int{{1, 2}, {3}}, 3, [][]int{{0, 1}, {2}}, true},
		{"empty group ignored", [][]int{{1}, {}, {2}}, 2, [][]int{{0}, {1}}, true},
		{"photo missing", [][]int{{1}}, 2, nil, false},
		{"photo twice", [][]int{{1, 2}, {2}}, 2, nil, false},
		{"out of range", [][]int{{1, 3}}, 2, nil, false},
		{"zero-based by mistake", [][]int{{0, 1}}, 2, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			docs := make([]Document, len(tt.groups))
			for i, g := range tt.groups {
				docs[i] = Document{Pages: g}
			}
			got, err := toZeroBased(docs, tt.n)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
			var pages [][]int
			for _, d := range got {
				pages = append(pages, d.Pages)
			}
			if tt.ok && !reflect.DeepEqual(pages, tt.want) {
				t.Fatalf("got %v, want %v", pages, tt.want)
			}
		})
	}
}

type request struct {
	Model    string `json:"model"`
	Messages []struct {
		Content []struct {
			Type     string            `json:"type"`
			Text     string            `json:"text"`
			ImageURL map[string]string `json:"image_url"`
		} `json:"content"`
	} `json:"messages"`
}

func (r request) images() int {
	n := 0
	for _, c := range r.Messages[0].Content {
		if c.Type == "image_url" {
			n++
		}
	}
	return n
}

func (r request) text() string {
	var b strings.Builder
	for _, c := range r.Messages[0].Content {
		b.WriteString(c.Text + "\n")
	}
	return b.String()
}

// fakeLLM answers every chat request with reply and records the request.
func fakeLLM(t *testing.T, reply string, got *request) *LLM {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		json.NewDecoder(r.Body).Decode(got)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return NewLLM(srv.URL+"/v1", "gemma")
}

func content(s string) string {
	b, _ := json.Marshal(s)
	return `{"choices":[{"message":{"content":` + string(b) + `}}]}`
}

func TestGroupSendsTimelineAndReadsContext(t *testing.T) {
	var got request
	llm := fakeLLM(t, content(`{"documents":[
		{"pages":[1],"context":"minha CNH"},
		{"pages":[2],"context":"carteira de vacinação"}]}`), &got)

	entries := []Entry{
		{Text: "minha CNH"},
		{Image: []byte("a"), MimeType: "image/jpeg"},
		{Text: "carteira de vacinação"},
		{Image: []byte("b"), MimeType: "image/jpeg", Text: "do bebê"},
	}
	docs, err := llm.Group(context.Background(), entries)
	if err != nil {
		t.Fatal(err)
	}
	want := []Document{{Pages: []int{0}, Context: "minha CNH"}, {Pages: []int{1}, Context: "carteira de vacinação"}}
	if !reflect.DeepEqual(docs, want) {
		t.Fatalf("docs = %+v", docs)
	}
	if got.Model != "gemma" || got.images() != 2 {
		t.Fatalf("model %q, %d images", got.Model, got.images())
	}
	text := got.text()
	// Texts must reach the model in the order they were sent.
	cnh, vac := strings.Index(text, "minha CNH"), strings.Index(text, "carteira de vacinação")
	if cnh < 0 || vac < cnh || !strings.Contains(text, "do bebê") {
		t.Errorf("prompt does not keep the timeline:\n%s", text)
	}
}

func TestGroupWithoutPhotosNeedsNoCall(t *testing.T) {
	docs, err := NewLLM("http://127.0.0.1:1", "x").Group(context.Background(), []Entry{{Text: "oi"}})
	if err != nil || docs != nil {
		t.Fatalf("got %v, %v", docs, err)
	}
}

func TestSameDocumentReadsYesProbability(t *testing.T) {
	var got request
	llm := fakeLLM(t, `{"choices":[{"message":{"content":"Yes"},"logprobs":{"content":[{"token":"Yes","logprob":-0.1,
		"top_logprobs":[{"token":"Yes","logprob":-0.1},{"token":"No","logprob":-2.4},{"token":" yes","logprob":-5},{"token":"Maybe","logprob":-6}]}]}}]}`, &got)

	p, err := llm.SameDocument(context.Background(), Recent{Text: "frente"}, Entry{Image: []byte("a"), MimeType: "image/jpeg"})
	if err != nil {
		t.Fatal(err)
	}
	yes, no := math.Exp(-0.1)+math.Exp(-5), math.Exp(-2.4)
	if math.Abs(p-yes/(yes+no)) > 1e-9 {
		t.Fatalf("p = %v, want %v", p, yes/(yes+no))
	}
	if got.images() != 1 || !strings.Contains(got.text(), "frente") {
		t.Errorf("request = %+v", got)
	}
}

func TestSameDocumentRejectsOtherAnswers(t *testing.T) {
	var got request
	llm := fakeLLM(t, `{"choices":[{"message":{"content":"Maybe"},"logprobs":{"content":[{"token":"Maybe","logprob":0,
		"top_logprobs":[{"token":"Maybe","logprob":0}]}]}}]}`, &got)
	if _, err := llm.SameDocument(context.Background(), Recent{Text: "x"}, Entry{Image: []byte("a")}); err == nil {
		t.Fatal("expected an error when the answer is neither yes nor no")
	}
}
