package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSummarizeSendsImageAndTextAndClips(t *testing.T) {
	var got struct {
		Messages []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	long := strings.Repeat("palavra ", 40)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		reply, _ := json.Marshal(map[string]string{"title": "  CNH do\nAndré ", "summary": long})
		content, _ := json.Marshal(string(reply))
		io.WriteString(w, `{"choices":[{"message":{"content":`+string(content)+`}}]}`)
	}))
	defer srv.Close()

	s, err := New(srv.URL, "gemma").Summarize(context.Background(), Document{Text: "HABILITAÇÃO", Image: []byte("x"), MimeType: "image/jpeg"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "CNH do André" {
		t.Errorf("title = %q", s.Title)
	}
	if n := len([]rune(s.Summary)); n != MaxSummary || !strings.HasSuffix(s.Summary, "…") {
		t.Errorf("summary has %d characters: %q", n, s.Summary)
	}
	c := got.Messages[0].Content
	if c[0].Type != "image_url" || !strings.Contains(c[1].Text, "HABILITAÇÃO") {
		t.Errorf("content = %+v", c)
	}
}

func TestSummarizeNeedsSomething(t *testing.T) {
	if _, err := New("http://127.0.0.1:1", "x").Summarize(context.Background(), Document{Text: "  "}); err == nil {
		t.Fatal("expected an error for an empty document")
	}
}
