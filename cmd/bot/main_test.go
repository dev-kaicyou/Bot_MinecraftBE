package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPI(t *testing.T) {
	b := &bot{}
	h := b.handler("test-secret")
	cases := []struct {
		method, path, body, token string
		want                      int
	}{
		{"GET", "/healthz", "", "", 200},
		{"GET", "/readyz", "", "", 503},
		{"GET", "/api/status", "", "", 401},
		{"GET", "/api/status", "", "test-secret", 200},
		{"POST", "/api/chat", `{"message":"hello"}`, "", 401},
		{"POST", "/api/chat", `{"message":""}`, "test-secret", 400},
		{"POST", "/api/chat", `{"message":"hello\nworld"}`, "test-secret", 400},
		{"POST", "/api/chat", `{"message":"hello"}`, "test-secret", 503},
		{"POST", "/api/chat", `{"unknown":true}`, "test-secret", 400},
	}
	for _, tc := range cases {
		t.Run(tc.method+tc.path+tc.body+tc.token, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestConfig(t *testing.T) {
	t.Setenv("MC_SERVER_ADDRESS", "localhost:19132")
	t.Setenv("API_TOKEN", "012345678901234567890123456789")
	t.Setenv("MC_AUTH_MODE", "offline")
	t.Setenv("RECONNECT_DELAY", "10s")
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RECONNECT_DELAY", "0s")
	if _, err := loadConfig(); err == nil {
		t.Fatal("accepted zero retry delay")
	}
}
