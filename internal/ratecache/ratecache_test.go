package ratecache

import (
	"errors"
	"testing"
	"time"
)

func TestCacheHit(t *testing.T) {
	c := New(time.Second)
	c.Update("EUR", 1.10, "bank")
	r, err := c.Get("EUR")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if r.Rate != 1.10 {
		t.Fatalf("rate = %v", r.Rate)
	}
	if r.Source != "bank" {
		t.Fatalf("source = %q", r.Source)
	}
}

func TestCacheNotFound(t *testing.T) {
	c := New(time.Second)
	_, err := c.Get("EUR")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestCacheStale(t *testing.T) {
	c := New(20 * time.Millisecond)
	c.Update("EUR", 1.10, "bank")
	time.Sleep(40 * time.Millisecond)
	_, err := c.Get("EUR")
	if !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
}

func TestCacheRefreshClearsStale(t *testing.T) {
	c := New(20 * time.Millisecond)
	c.Update("EUR", 1.10, "bank")
	time.Sleep(40 * time.Millisecond)
	c.Update("EUR", 1.11, "venue")
	r, err := c.Get("EUR")
	if err != nil {
		t.Fatalf("get after refresh: %v", err)
	}
	if r.Rate != 1.11 {
		t.Fatalf("rate = %v, want 1.11", r.Rate)
	}
	if r.Source != "venue" {
		t.Fatalf("source = %q", r.Source)
	}
}

func TestCrossCheck(t *testing.T) {
	c := New(time.Second)
	c.Update("EUR", 1.10, "bank")
	c.SetRevaluationRate("EUR", 1.10)
	if !c.CrossCheck("EUR", 5) {
		t.Fatal("expected cross-check true for identical rates")
	}
	c.Update("EUR", 1.11, "venue") // ~90 bps diff
	if c.CrossCheck("EUR", 5) {
		t.Fatal("expected cross-check false for large diff")
	}
	if !c.CrossCheck("EUR", 200) {
		t.Fatal("expected cross-check true within 200 bps tolerance")
	}
}

func TestRevaluationRateUnset(t *testing.T) {
	c := New(time.Second)
	if c.RevaluationRate("EUR") != 0 {
		t.Fatal("expected 0 for unset reval rate")
	}
	if c.CrossCheck("EUR", 100) {
		t.Fatal("expected false with no reval rate")
	}
}

func TestCacheTTLZeroNeverExpires(t *testing.T) {
	c := New(0)
	c.Update("EUR", 1.10, "bank")
	time.Sleep(20 * time.Millisecond)
	if _, err := c.Get("EUR"); err != nil {
		t.Fatalf("zero TTL should never expire: %v", err)
	}
}

func TestCacheTTLReturnsConfigured(t *testing.T) {
	c := New(123 * time.Millisecond)
	if got := c.TTL(); got != 123*time.Millisecond {
		t.Fatalf("TTL = %v, want 123ms", got)
	}
}

func TestRevaluationRateSet(t *testing.T) {
	c := New(time.Second)
	c.SetRevaluationRate("EUR", 1.10)
	if r := c.RevaluationRate("EUR"); r != 1.10 {
		t.Fatalf("reval rate = %v, want 1.10", r)
	}
}

func TestCrossCheckMissingLiveRate(t *testing.T) {
	c := New(time.Second)
	c.SetRevaluationRate("EUR", 1.10)
	if c.CrossCheck("EUR", 100) {
		t.Fatal("expected false when live rate missing")
	}
}