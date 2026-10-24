package paperless

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, mux *http.ServeMux) *Client {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New(srv.URL, "secret")
	c.pollInterval = time.Millisecond
	return c
}

func TestWaitForAIWaitsForTheDocumentsTask(t *testing.T) {
	var polls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tasks/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("task_type") != "apply_ai_suggestions" {
			http.Error(w, "wrong filter", http.StatusBadRequest)
			return
		}
		status := "started"
		if polls.Add(1) > 2 {
			status = "success"
		}
		io.WriteString(w, `{"results":[
			{"status":"started","input_data":{"document_id":7}},
			{"status":"`+status+`","input_data":{"document_id":8}}]}`)
	})
	if err := newTestClient(t, mux).WaitForAI(context.Background(), 8, time.Hour); err != nil {
		t.Fatal(err)
	}
	if polls.Load() != 3 {
		t.Errorf("polled %d times, want 3", polls.Load())
	}
}

func TestWaitForAIGivesUpWithoutATask(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tasks/", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"results":[]}`)
	})
	if err := newTestClient(t, mux).WaitForAI(context.Background(), 8, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestShareClearsOwner(t *testing.T) {
	var patched atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tags/4/", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":4,"owner":5}`)
	})
	mux.HandleFunc("GET /api/tags/1/", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":1,"owner":null}`)
	})
	mux.HandleFunc("PATCH /api/tags/4/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"owner":null}` {
			http.Error(w, "unexpected body "+string(body), http.StatusBadRequest)
			return
		}
		patched.Store(true)
		io.WriteString(w, `{}`)
	})
	c := newTestClient(t, mux)
	if changed, err := c.Share(context.Background(), "tags", 4); err != nil || !changed || !patched.Load() {
		t.Fatalf("Share(4) = %v, %v", changed, err)
	}
	if changed, err := c.Share(context.Background(), "tags", 1); err != nil || changed {
		t.Fatalf("Share(1) = %v, %v", changed, err)
	}
}

func TestCustomFieldID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/custom_fields/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"results":[{"id":3,"name":"Resumo"}]}`)
	})
	c := newTestClient(t, mux)
	if id, err := c.CustomFieldID(context.Background(), "resumo"); err != nil || id != 3 {
		t.Fatalf("got %d, %v", id, err)
	}
}
