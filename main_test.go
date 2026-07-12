package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	healthz(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", got)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf(`expected status "ok", got %q`, body["status"])
	}
}

func TestNewMux(t *testing.T) {
	srv := httptest.NewServer(newMux())
	defer srv.Close()

	tests := []struct {
		name     string
		path     string
		wantCode int
	}{
		{name: "healthz route", path: "/healthz", wantCode: http.StatusOK},
		{name: "unknown route", path: "/nope", wantCode: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := http.Get(srv.URL + tt.path)
			if err != nil {
				t.Fatalf("GET %s: %v", tt.path, err)
			}
			defer resp.Body.Close()
			if _, err := io.Copy(io.Discard, resp.Body); err != nil {
				t.Fatalf("reading body: %v", err)
			}
			if resp.StatusCode != tt.wantCode {
				t.Errorf("GET %s: expected %d, got %d", tt.path, tt.wantCode, resp.StatusCode)
			}
		})
	}
}

func TestRunInvalidAddr(t *testing.T) {
	if err := run("256.256.256.256:-1"); err == nil {
		t.Fatal("expected error for invalid listen address, got nil")
	}
}
