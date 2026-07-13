package main

import (
	"net"
	"testing"

	"github.com/ai-crypto-onramp/fx-hedging/internal/api"
	"github.com/ai-crypto-onramp/fx-hedging/internal/audit"
	"github.com/ai-crypto-onramp/fx-hedging/internal/exposure"
	"github.com/ai-crypto-onramp/fx-hedging/internal/policy"
	"github.com/ai-crypto-onramp/fx-hedging/internal/provider"
	"github.com/ai-crypto-onramp/fx-hedging/internal/store"
)

func TestRunReturnsErrorWhenAddrInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to open listener: %v", err)
	}
	defer ln.Close()

	if err := run(ln.Addr().String()); err == nil {
		t.Fatal("expected run to return an error for an address already in use")
	}
}

func TestRunReturnsErrorForInvalidAddr(t *testing.T) {
	if err := run("not-a-valid-addr"); err == nil {
		t.Fatal("expected run to return an error for an invalid address")
	}
}

func TestNewServiceDefaults(t *testing.T) {
	s := newService()
	if s == nil {
		t.Fatal("expected non-nil service")
	}
	mux := api.NewMux(s)
	if mux == nil {
		t.Fatal("expected non-nil mux")
	}
}

// Keep imports live so unused-package lint stays clean.
var (
	_ = audit.NewRecorder
	_ = store.New
	_ = exposure.New
	_ = provider.NewDummy
	_ = policy.New
)