package exposure

import (
	"context"
	"sync"
	"time"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
)

// SnapshotSink consumes exposure snapshots. store.Store implements it via
// AppendExposureSnapshot.
type SnapshotSink interface {
	AppendExposureSnapshot(*domain.Exposure)
}

// Snapshotter persists exposure snapshots to a sink on each change (via the
// tracker subscriber channel) and on a configurable refresh interval tick.
// It is safe for concurrent use. Run blocks until ctx is cancelled.
type Snapshotter struct {
	tracker   *Tracker
	sink      SnapshotSink
	interval  time.Duration
	ch        chan *domain.Exposure
	stopOnce  sync.Once
	stoppedCh chan struct{}
}

// NewSnapshotter returns a Snapshotter that persists snapshots from tr to
// sink on the given refresh interval (and on each change).
func NewSnapshotter(tr *Tracker, sink SnapshotSink, interval time.Duration) *Snapshotter {
	if interval <= 0 {
		interval = time.Second
	}
	ch := make(chan *domain.Exposure, 64)
	tr.Subscribe(ch)
	return &Snapshotter{
		tracker:   tr,
		sink:      sink,
		interval:  interval,
		ch:        ch,
		stoppedCh: make(chan struct{}),
	}
}

// Run blocks until ctx is cancelled, persisting snapshots as they arrive
// and on each interval tick (persisting the current snapshot for every
// known currency). It is safe to call Run in a background goroutine.
func (s *Snapshotter) Run(ctx context.Context) {
	defer close(s.stoppedCh)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-s.ch:
			s.sink.AppendExposureSnapshot(e)
		case <-ticker.C:
			for _, e := range s.tracker.AllExposures() {
				s.sink.AppendExposureSnapshot(e)
			}
		}
	}
}

// Stop signals the snapshotter to stop and waits for it to drain. It is
// idempotent.
func (s *Snapshotter) Stop() {
	s.stopOnce.Do(func() {
		s.tracker.Unsubscribe(s.ch)
	})
	<-s.stoppedCh
}