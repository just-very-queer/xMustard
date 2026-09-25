package httpapi

import "net/http"

// HandleHealthCheck reports liveness for load balancers.
func HandleHealthCheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// HandleNotFound answers unknown routes.
func HandleNotFound(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}
