package main

import (
	"encoding/json"
	"net/http"
)

func main() {
	http.HandleFunc("/healthz", healthz)
	_ = http.ListenAndServe(":8080", nil)
}

func healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}