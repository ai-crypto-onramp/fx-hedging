package main

import (
	"log"
	"net/http"
	"os"

	"github.com/ai-crypto-onramp/fx-hedging/internal/api"
	"github.com/ai-crypto-onramp/fx-hedging/internal/audit"
	"github.com/ai-crypto-onramp/fx-hedging/internal/exposure"
	"github.com/ai-crypto-onramp/fx-hedging/internal/policy"
	"github.com/ai-crypto-onramp/fx-hedging/internal/provider"
	"github.com/ai-crypto-onramp/fx-hedging/internal/store"
)

func main() {
	log.Fatal(run(":8080"))
}

// run starts the HTTP server on addr and blocks until it exits.
func run(addr string) error {
	srv := newService()
	return http.ListenAndServe(addr, api.NewMux(srv))
}

// newService wires the service dependencies from environment configuration.
func newService() *api.Service {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	_ = port

	st := store.New()
	tr := exposure.New()
	p := provider.NewDummy()
	pol := policy.New()
	rec := audit.NewRecorder()
	return api.NewService(st, tr, p, pol, rec)
}