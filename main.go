package main

import (
	"encoding/json"
	"net/http"
)

func main() {
	_ = run(":8080")
}

// run starts the HTTP server on the given address and blocks until it exits.
func run(addr string) error {
	return http.ListenAndServe(addr, newMux())
}

// newMux builds the service's HTTP routing table.
func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)
	return mux
}

func healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
