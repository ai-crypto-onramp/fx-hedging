package settlement

import (
	"testing"
	"time"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
)

func TestNetOffsetsSameCurrency(t *testing.T) {
	e := New()
	legs := []Leg{
		{Currency: "EUR", Amount: 100_000, Source: "flow", At: time.UnixMilli(100)},
		{Currency: "EUR", Amount: -100_000, Source: "hedge", At: time.UnixMilli(200)},
	}
	ob := e.Net(legs)
	if len(ob) != 0 {
		t.Fatalf("expected fully offset (0 obligations), got %d", len(ob))
	}
}

func TestNetPartialOffset(t *testing.T) {
	e := New()
	legs := []Leg{
		{Currency: "EUR", Amount: 100_000, Source: "flow", At: time.UnixMilli(100)},
		{Currency: "EUR", Amount: -40_000, Source: "hedge", At: time.UnixMilli(200)},
	}
	ob := e.Net(legs)
	if len(ob) != 1 {
		t.Fatalf("len = %d, want 1", len(ob))
	}
	if ob[0].Currency != "EUR" {
		t.Fatalf("currency = %q", ob[0].Currency)
	}
	if ob[0].Amount != 60_000 {
		t.Fatalf("amount = %v, want 60000", ob[0].Amount)
	}
	if ob[0].Legs != 2 {
		t.Fatalf("legs = %d, want 2", ob[0].Legs)
	}
}

func TestNetMultipleCurrencies(t *testing.T) {
	e := New()
	legs := []Leg{
		{Currency: "EUR", Amount: 100_000, Source: "flow"},
		{Currency: "JPY", Amount: -50_000, Source: "flow"},
	}
	ob := e.Net(legs)
	if len(ob) != 2 {
		t.Fatalf("len = %d, want 2", len(ob))
	}
	// Sorted by currency code.
	if ob[0].Currency != "EUR" {
		t.Fatalf("first = %q, want EUR", ob[0].Currency)
	}
	if ob[1].Currency != "JPY" {
		t.Fatalf("second = %q, want JPY", ob[1].Currency)
	}
}

func TestNetFromFlowsAndHedges(t *testing.T) {
	e := New()
	flows := []domain.Exposure{
		{Currency: "EUR", NetAmount: 100_000, UpdatedAt: time.UnixMilli(100)},
		{Currency: "JPY", NetAmount: -30_000, UpdatedAt: time.UnixMilli(100)},
	}
	hedges := []*domain.Hedge{
		{Currency: "EUR", Notional: 90_000, Status: domain.StatusExecuted, UpdatedAt: time.UnixMilli(200)},
	}
	ob := e.NetFromFlowsAndHedges(flows, hedges)
	got := map[string]float64{}
	for _, o := range ob {
		got[o.Currency] = o.Amount
	}
	// EUR: 100k flow + (-90k hedge) = 10k net.
	if got["EUR"] != 10_000 {
		t.Fatalf("EUR net = %v, want 10000", got["EUR"])
	}
	// JPY: -30k flow, no hedge.
	if got["JPY"] != -30_000 {
		t.Fatalf("JPY net = %v, want -30000", got["JPY"])
	}
}

func TestNetIgnoresNonExecutedHedges(t *testing.T) {
	e := New()
	flows := []domain.Exposure{{Currency: "EUR", NetAmount: 100_000, UpdatedAt: time.UnixMilli(100)}}
	hedges := []*domain.Hedge{
		{Currency: "EUR", Notional: 90_000, Status: domain.StatusFailed, UpdatedAt: time.UnixMilli(200)},
	}
	ob := e.NetFromFlowsAndHedges(flows, hedges)
	if len(ob) != 1 || ob[0].Amount != 100_000 {
		t.Fatalf("expected 100k net (failed hedge ignored), got %v", ob)
	}
}