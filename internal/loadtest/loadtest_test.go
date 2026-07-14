// Package loadtest contains in-process stress tests that validate the
// exposure latency (< 2s) and spot execution latency (< 500 ms) SLOs
// under a burst of flow events. These are unit-level tests that exercise
// the in-memory tracker, router, and latency executor without external
// dependencies.
package loadtest

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
	"github.com/ai-crypto-onramp/fx-hedging/internal/executor"
	"github.com/ai-crypto-onramp/fx-hedging/internal/exposure"
)

func TestExposureLatencyUnderBurst(t *testing.T) {
	tr := exposure.New()
	const n = 500
	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			tr.AddExposure("EUR", float64(i))
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("exposure burst took %v, want < 2s", elapsed)
	}
	got := tr.GetExposure("EUR")
	wantSum := 0.0
	for i := 0; i < n; i++ {
		wantSum += float64(i)
	}
	if got.NetAmount != wantSum {
		t.Fatalf("net = %v, want %v", got.NetAmount, wantSum)
	}
	t.Logf("processed %d events in %v", n, elapsed)
}

func TestSpotExecutionLatencyUnderBurst(t *testing.T) {
	bank := executor.NewBankAdapter(1.10)
	le := executor.NewLatencyExecutor(bank, 500*time.Millisecond)
	rtr := executor.NewRouter(le)
	const n = 100
	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			h := &domain.Hedge{
				ID:        fmt.Sprintf("h-%d", i),
				Currency:  "EUR",
				Notional:  1000,
				Tenor:     domain.TenorSpot,
				Type:      domain.TypeSpot,
				QuotedRate: 1.10,
			}
			if _, err := rtr.RouteAndExecute(context.Background(), h); err != nil {
				t.Errorf("exec %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)
	count, exceeded, _ := le.Stats()
	if count != int64(n) {
		t.Fatalf("count = %d, want %d", count, n)
	}
	if exceeded > 0 {
		t.Fatalf("%d spot orders exceeded the 500ms SLO", exceeded)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("%d spot orders took %v total, want < 2s wall clock", n, elapsed)
	}
	t.Logf("executed %d spot orders in %v (0 exceeded SLO)", n, elapsed)
}