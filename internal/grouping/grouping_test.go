package grouping

import (
	"context"
	"encoding/json"
	"io"
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
			got, err := toZeroBased(tt.groups, tt.n)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
			if tt.ok && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGroupSendsImagesAndContext(t *testing.T) {
	var got struct {
		Model    string `json:"model"`
		Messages []struct {
			Content []struct {
				Type     string            `json:"type"`
				Text     string            `json:"text"`
				ImageURL map[string]string `json:"image_url"`
			} `json:"content"`
		} `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"documents\":[[2,1]]}"}}]}`)
	}))
	defer srv.Close()

	pages := []Page{
		{Image: []byte("a"), MimeType: "image/jpeg"},
		{Image: []byte("b"), MimeType: "image/jpeg", Caption: "verso"},
	}
	groups, err := NewLLM(srv.URL+"/v1", "gemma").Group(context.Background(), pages, "minha CNH")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(groups, [][]int{{1, 0}}) {
		t.Fatalf("groups = %v", groups)
	}

	var images int
	var text strings.Builder
	for _, c := range got.Messages[0].Content {
		if c.Type == "image_url" {
			images++
			if !strings.HasPrefix(c.ImageURL["url"], "data:image/jpeg;base64,") {
				t.Errorf("image url = %q", c.ImageURL["url"])
			}
		}
		text.WriteString(c.Text)
	}
	if got.Model != "gemma" || images != 2 {
		t.Fatalf("model %q, %d images", got.Model, images)
	}
	for _, want := range []string{"minha CNH", "verso"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("prompt is missing %q", want)
		}
	}
}

func TestSinglePageNeedsNoCall(t *testing.T) {
	groups, err := NewLLM("http://127.0.0.1:1", "x").Group(context.Background(), []Page{{}}, "")
	if err != nil || !reflect.DeepEqual(groups, [][]int{{0}}) {
		t.Fatalf("got %v, %v", groups, err)
	}
}
