package paperless

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakePaperless serves the handful of endpoints the client uses. The task
// stays pending for one poll, and the document is renamed by "the AI" after
// it's been read twice.
func fakePaperless(t *testing.T, taskStatus string) *httptest.Server {
	t.Helper()
	var taskPolls, docReads atomic.Int32
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
	mux.HandleFunc("GET /api/documents/42/", func(w http.ResponseWriter, r *http.Request) {
		if docReads.Add(1) <= 2 {
			io.WriteString(w, `{"id":42,"title":"conta","tags":[],"correspondent":null}`)
			return
		}
		io.WriteString(w, `{"id":42,"title":"Fatura Enel - Setembro/2026","tags":[1,2],"correspondent":7}`)
	})
	mux.HandleFunc("GET /api/tags/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id__in") != "1,2" {
			http.Error(w, "wrong tags", http.StatusBadRequest)
			return
		}
		io.WriteString(w, `{"results":[{"name":"Contas"},{"name":"Casa"}]}`)
	})
	mux.HandleFunc("GET /api/correspondents/7/", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"name":"Enel"}`)
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

func TestUploadWaitAndRead(t *testing.T) {
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
	doc, err := c.WaitForTitleChange(ctx, id, "conta")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "Fatura Enel - Setembro/2026" || doc.Correspondent != "Enel" || len(doc.Tags) != 2 || doc.Tags[0] != "Contas" {
		t.Fatalf("unexpected document: %+v", doc)
	}
}

func TestWaitForDocumentFailure(t *testing.T) {
	c := newTestClient(fakePaperless(t, "failure").URL)
	_, err := c.WaitForDocument(context.Background(), "task-123")
	if !errors.Is(err, ErrConsumeFailed) {
		t.Fatalf("err = %v, want ErrConsumeFailed", err)
	}
}

func TestWaitForTitleChangeReturnsLatestOnTimeout(t *testing.T) {
	c := newTestClient(fakePaperless(t, "success").URL)
	c.pollInterval = time.Hour // never gets to the renamed version
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	doc, err := c.WaitForTitleChange(ctx, 42, "conta")
	if err != nil || doc.Title != "conta" {
		t.Fatalf("got %+v, %v; want the un-renamed document and no error", doc, err)
	}
}

func TestUploadRejectsBadToken(t *testing.T) {
	c := New(fakePaperless(t, "success").URL, "wrong")
	if _, err := c.Upload(context.Background(), "conta.pdf", []byte("%PDF")); err == nil {
		t.Fatal("expected an error for a bad token")
	}
}
