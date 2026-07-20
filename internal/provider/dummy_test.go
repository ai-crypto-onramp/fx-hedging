package provider

import (
	"errors"
	"testing"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
)

func TestNewDummyDefaults(t *testing.T) {
	d := NewDummy()
	if d.Rate != 1.10 {
		t.Fatalf("default rate = %v, want 1.10", d.Rate)
	}
	if d.FailExecute {
		t.Fatal("FailExecute should be false by default")
	}
}

func TestQuoteAndExecute(t *testing.T) {
	d := &DummyFXProvider{Rate: 1.20}
	rate, err := d.Quote("EUR", 100_000, "spot")
	if err != nil {
		t.Fatalf("quote err: %v", err)
	}
	if rate != 1.20 {
		t.Fatalf("rate = %v, want 1.20", rate)
	}

	h := &domain.Hedge{ID: "h1", Currency: "EUR", Notional: 100_000, QuotedRate: 1.20}
	fills, err := d.Execute(h)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	if len(fills) != 1 {
		t.Fatalf("fills len = %d, want 1", len(fills))
	}
	if fills[0].Price != 1.20 {
		t.Fatalf("fill price = %v, want 1.20", fills[0].Price)
	}
	if fills[0].Amount != 100_000 {
		t.Fatalf("fill amount = %v, want 100000", fills[0].Amount)
	}
	if fills[0].HedgeID != "h1" {
		t.Fatalf("hedge id = %q, want h1", fills[0].HedgeID)
	}
	if fills[0].VenueTradeID == "" {
		t.Fatal("venue trade id should be set")
	}
	samples := d.Samples()
	if len(samples) != 1 {
		t.Fatalf("samples len = %d, want 1", len(samples))
	}
	if samples[0].SlippageBPS != 0 {
		t.Fatalf("slippage = %v, want 0", samples[0].SlippageBPS)
	}
}

func TestExecuteWithSlippage(t *testing.T) {
	d := &DummyFXProvider{Rate: 1.0, SlippageBPS: 10}
	h := &domain.Hedge{ID: "h1", Currency: "EUR", Notional: 100, QuotedRate: 1.0}
	fills, err := d.Execute(h)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	want := 1.0 * (1 + 10.0/10_000.0)
	if fills[0].Price != want {
		t.Fatalf("price = %v, want %v", fills[0].Price, want)
	}
	samples := d.Samples()
	if samples[0].SlippageBPS != 10 {
		t.Fatalf("slippage bps = %v, want 10", samples[0].SlippageBPS)
	}
}

func TestQuoteFail(t *testing.T) {
	d := &DummyFXProvider{Rate: 1.0, FailExecute: true}
	_, err := d.Quote("EUR", 100, "spot")
	if !errors.Is(err, ErrQuoteFailed) {
		t.Fatalf("err = %v, want ErrQuoteFailed", err)
	}
}

func TestExecuteFail(t *testing.T) {
	d := &DummyFXProvider{Rate: 1.0, FailExecute: true}
	h := &domain.Hedge{ID: "h1", QuotedRate: 1.0}
	_, err := d.Execute(h)
	if !errors.Is(err, ErrExecuteFailed) {
		t.Fatalf("err = %v, want ErrExecuteFailed", err)
	}
}

func TestSamplesReturnsCopy(t *testing.T) {
	d := &DummyFXProvider{Rate: 1.0}
	h := &domain.Hedge{ID: "h1", Currency: "EUR", Notional: 100, QuotedRate: 1.0}
	_, _ = d.Execute(h)
	s := d.Samples()
	if len(s) != 1 {
		t.Fatalf("len = %d, want 1", len(s))
	}
	s[0].Pair = "mutated"
	again := d.Samples()
	if again[0].Pair == "mutated" {
		t.Fatal("Samples should return a copy")
	}
}

func TestNewDummyEnvConfig(t *testing.T) {
	t.Setenv("DUMMY_FX_RATE", "2.5")
	t.Setenv("DUMMY_FX_SLIPPAGE_BPS", "7")
	t.Setenv("DUMMY_FX_FAIL", "1")
	d := NewDummy()
	if d.Rate != 2.5 {
		t.Fatalf("rate = %v, want 2.5", d.Rate)
	}
	if d.SlippageBPS != 7 {
		t.Fatalf("slippage = %v, want 7", d.SlippageBPS)
	}
	if !d.FailExecute {
		t.Fatal("FailExecute should be true when DUMMY_FX_FAIL=1")
	}
}

func TestNewDummyEnvInvalidValues(t *testing.T) {
	t.Setenv("DUMMY_FX_RATE", "not-a-number")
	t.Setenv("DUMMY_FX_SLIPPAGE_BPS", "bad")
	t.Setenv("DUMMY_FX_FAIL", "")
	d := NewDummy()
	if d.Rate != 1.10 {
		t.Fatalf("rate = %v, want default 1.10", d.Rate)
	}
	if d.SlippageBPS != 0 {
		t.Fatalf("slippage = %v, want 0", d.SlippageBPS)
	}
	if d.FailExecute {
		t.Fatal("FailExecute should be false by default")
	}
}

func TestNewDummyEnvNegativeRate(t *testing.T) {
	t.Setenv("DUMMY_FX_RATE", "-1.0")
	d := NewDummy()
	if d.Rate != 1.10 {
		t.Fatalf("negative rate should fall back to default; got %v", d.Rate)
	}
}