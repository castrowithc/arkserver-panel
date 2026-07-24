package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouter(t *testing.T) {
	router := newRouter(config{user: "admin", pass: "secret"})

	tests := []struct {
		name       string
		path       string
		auth       bool
		user, pass string
		want       int
	}{
		{"health is open", "/healthz", false, "", "", http.StatusOK},
		{"root needs auth", "/", false, "", "", http.StatusUnauthorized},
		{"wrong pass rejected", "/", true, "admin", "nope", http.StatusUnauthorized},
		{"correct creds ok", "/", true, "admin", "secret", http.StatusOK},
		{"unknown path 404", "/nope", true, "admin", "secret", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.auth {
				req.SetBasicAuth(tt.user, tt.pass)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Errorf("%s %s: want %d, got %d", req.Method, tt.path, tt.want, rec.Code)
			}
		})
	}
}
