package main

import (
	"crypto/subtle"
	"io"
	"net/http"
	"strings"
)

func AuthMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fields := strings.Fields(r.Header.Get("Authorization"))
		if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") || token == "" ||
			subtle.ConstantTimeCompare([]byte(fields[1]), []byte(token)) != 1 {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"未授权，请重新配对"}`)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
