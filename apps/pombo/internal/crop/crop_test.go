package crop

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCrop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crop" || r.Header.Get("Content-Type") != "image/png" {
			t.Errorf("got %s %s", r.URL.Path, r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		switch string(body) {
		case "photo":
			w.Write([]byte("page"))
		case "scan":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "not an image", http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	c := New(srv.URL + "/")

	got, err := c.Crop(context.Background(), []byte("photo"), "image/png")
	if err != nil || !bytes.Equal(got, []byte("page")) {
		t.Fatalf("photo: %q, %v", got, err)
	}
	if _, err := c.Crop(context.Background(), []byte("scan"), "image/png"); !errors.Is(err, ErrNoPage) {
		t.Fatalf("scan: want ErrNoPage, got %v", err)
	}
	if _, err := c.Crop(context.Background(), []byte("junk"), "image/png"); err == nil || errors.Is(err, ErrNoPage) {
		t.Fatalf("junk: want an error, got %v", err)
	}
}
