package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthenticationGuardsHandler(t *testing.T) {
	const token = "test-secret-with-at-least-32-chars"
	for _, test := range []struct {
		name, authorization string
		allowed             bool
	}{
		{"missing", "", false},
		{"wrong", "Bearer not-the-token", false},
		{"wrong scheme", "Basic " + token, false},
		{"extra token", "Bearer " + token + " ignored", false},
		{"correct", "Bearer " + token, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			})
			request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
			request.Header.Set("Authorization", test.authorization)
			response := httptest.NewRecorder()
			AuthMiddleware(token, next).ServeHTTP(response, request)
			if called != test.allowed {
				t.Fatalf("protected handler called=%v, want %v", called, test.allowed)
			}
			if !test.allowed && response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d, want 401", response.Code)
			}
			if strings.Contains(response.Body.String(), token) {
				t.Fatal("authentication response disclosed the management token")
			}
		})
	}
}
