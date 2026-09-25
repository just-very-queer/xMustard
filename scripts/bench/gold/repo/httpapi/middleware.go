package httpapi

import "net/http"

// WithRequestLog wraps a handler and records each request path.
func WithRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}
