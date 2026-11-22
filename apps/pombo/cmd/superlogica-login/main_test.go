package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitForCodePollsUntilTheEmailArrives(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.Contains(r.URL.RawQuery, "since=") {
			t.Errorf("no since in %s", r.URL)
		}
		if calls.Add(1) < 2 {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"no email yet"}`))
			return
		}
		w.Write([]byte(`{"code":"482913"}`))
	}))
	defer srv.Close()

	cfg := config{OTPURL: srv.URL, OTPToken: "tok", OTPTimeout: 10 * time.Second}
	code, err := waitForCode(context.Background(), cfg, time.Now())
	if err != nil || code != "482913" {
		t.Fatalf("got %q, %v", code, err)
	}
	cfg.OTPToken = "wrong"
	if _, err := waitForCode(context.Background(), cfg, time.Now()); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("bad token: got %v", err)
	}
}
