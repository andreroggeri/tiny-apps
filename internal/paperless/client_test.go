package paperless

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakePaperless serves the handful of endpoints the client uses. The task
// stays pending for one poll.
func fakePaperless(t *testing.T, taskStatus string) *httptest.Server {
	t.Helper()
	var taskPolls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/documents/post_document/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		f, hdr, err := r.FormFile("document")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data, _ := io.ReadAll(f)
		if hdr.Filename != "conta.pdf" || string(data) != "%PDF" {
			http.Error(w, "unexpected upload", http.StatusBadRequest)
			return
		}
		io.WriteString(w, `"task-123"`)
	})
	mux.HandleFunc("GET /api/tasks/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("task_id") != "task-123" {
			http.Error(w, "wrong task", http.StatusBadRequest)
			return
		}
		if taskPolls.Add(1) == 1 {
			io.WriteString(w, `{"results":[{"status":"started","related_document_ids":[]}]}`)
			return
		}
		io.WriteString(w, `{"results":[{"status":"`+taskStatus+`","related_document_ids":[42],"result_data":{"error":"bad pdf"}}]}`)
	})
	mux.HandleFunc("POST /api/documents/42/notes/", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Note string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Note != "carteira de vacinação" {
			http.Error(w, "unexpected note", http.StatusBadRequest)
			return
		}
		io.WriteString(w, `[{"id":1,"note":"carteira de vacinação"}]`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestClient(url string) *Client {
	c := New(url, "secret")
	c.pollInterval = time.Millisecond
	return c
}

func TestUploadWaitAndNote(t *testing.T) {
	c := newTestClient(fakePaperless(t, "success").URL)
	ctx := context.Background()

	taskID, err := c.Upload(ctx, "conta.pdf", []byte("%PDF"))
	if err != nil || taskID != "task-123" {
		t.Fatalf("Upload = %q, %v", taskID, err)
	}
	id, err := c.WaitForDocument(ctx, taskID)
	if err != nil || id != 42 {
		t.Fatalf("WaitForDocument = %d, %v", id, err)
	}
	if err := c.AddNote(ctx, id, "carteira de vacinação"); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForDocumentFailure(t *testing.T) {
	c := newTestClient(fakePaperless(t, "failure").URL)
	_, err := c.WaitForDocument(context.Background(), "task-123")
	if !errors.Is(err, ErrConsumeFailed) {
		t.Fatalf("err = %v, want ErrConsumeFailed", err)
	}
}

func TestUploadRejectsBadToken(t *testing.T) {
	c := New(fakePaperless(t, "success").URL, "wrong")
	if _, err := c.Upload(context.Background(), "conta.pdf", []byte("%PDF")); err == nil {
		t.Fatal("expected an error for a bad token")
	}
}
