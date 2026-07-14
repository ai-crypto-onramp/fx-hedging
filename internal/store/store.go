package store

import (
	"sort"
	"sync"
	"time"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
)

// Store is a thread-safe in-memory store of hedges and slippage samples.
type Store struct {
	mu       sync.RWMutex
	hedges   map[string]*domain.Hedge
	byCcy    map[string][]string // currency -> hedge ids
	byReq    map[string]string   // client_request_id -> hedge id
	samples  []domain.SlippageSample
	pnlRows  []domain.PnL
	expRows  []domain.Exposure
}

// New returns an empty in-memory store.
func New() *Store {
	return &Store{
		hedges: make(map[string]*domain.Hedge),
		byCcy:  make(map[string][]string),
		byReq:  make(map[string]string),
	}
}

// CreateHedge stores h. It does not check for duplicates.
func (s *Store) CreateHedge(h *domain.Hedge) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hedges[h.ID] = h
	s.byCcy[h.Currency] = append(s.byCcy[h.Currency], h.ID)
	if h.ClientRequestID != "" {
		s.byReq[h.ClientRequestID] = h.ID
	}
}

// GetHedgeByClientRequest returns the hedge previously stored with the given
// client request id, or nil if none. Used for idempotent submission: a
// duplicate submission with the same client request id returns the original.
func (s *Store) GetHedgeByClientRequest(reqID string) *domain.Hedge {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byReq[reqID]
	if !ok {
		return nil
	}
	return cloneHedge(s.hedges[id])
}

// HasFill returns true if a fill with the given (venue, venueTradeID) is
// already recorded on any hedge. Used for idempotent fill callbacks.
func (s *Store) HasFill(venue, venueTradeID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if venue == "" || venueTradeID == "" {
		return false
	}
	for _, h := range s.hedges {
		for _, f := range h.Fills {
			if f.Venue == venue && f.VenueTradeID == venueTradeID {
				return true
			}
		}
	}
	return false
}

// GetHedge returns a deep copy of the hedge with id, or nil if not found.
func (s *Store) GetHedge(id string) *domain.Hedge {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.hedges[id]
	if !ok {
		return nil
	}
	return cloneHedge(h)
}

// UpdateHedge applies fn to the hedge with id while holding the write lock,
// persisting the result. Returns the updated hedge and the error from fn.
// If the hedge is not found, fn is not called and ErrNotFound is returned.
func (s *Store) UpdateHedge(id string, fn func(*domain.Hedge) error) (*domain.Hedge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.hedges[id]
	if !ok {
		return nil, ErrNotFound
	}
	if err := fn(h); err != nil {
		return nil, err
	}
	return cloneHedge(h), nil
}

// HedgesByCurrency returns deep copies of all hedges for the given currency,
// in created-at ascending order.
func (s *Store) HedgesByCurrency(currency string) []*domain.Hedge {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := s.byCcy[currency]
	out := make([]*domain.Hedge, 0, len(ids))
	for _, id := range ids {
		out = append(out, cloneHedge(s.hedges[id]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// AllHedges returns deep copies of all hedges, in created-at ascending order.
func (s *Store) AllHedges() []*domain.Hedge {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*domain.Hedge, 0, len(s.hedges))
	for _, h := range s.hedges {
		out = append(out, cloneHedge(h))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// AddSlippageSample appends a slippage sample.
func (s *Store) AddSlippageSample(sample domain.SlippageSample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.samples = append(s.samples, sample)
}

// AddPnL appends a P&L entry.
func (s *Store) AddPnL(p domain.PnL) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pnlRows = append(s.pnlRows, p)
}

// AllPnL returns all stored P&L entries.
func (s *Store) AllPnL() []domain.PnL {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.PnL, len(s.pnlRows))
	copy(out, s.pnlRows)
	return out
}

// AppendExposureSnapshot appends an exposure snapshot row (persisted view).
func (s *Store) AppendExposureSnapshot(e *domain.Exposure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expRows = append(s.expRows, *e)
}

// ExposureSnapshots returns stored exposure snapshots for a currency
// (empty = all) ordered by UpdatedAt ascending.
func (s *Store) ExposureSnapshots(currency string) []domain.Exposure {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Exposure, 0, len(s.expRows))
	for _, e := range s.expRows {
		if currency != "" && e.Currency != currency {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
	return out
}

// SlippageSamples returns samples filtered by pair (empty = all) and the
// [from, to] time range (zero values = unbounded on that side).
func (s *Store) SlippageSamples(pair string, from, to time.Time) []domain.SlippageSample {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.SlippageSample, 0, len(s.samples))
	for _, sm := range s.samples {
		if pair != "" && sm.Pair != pair {
			continue
		}
		if !from.IsZero() && sm.Timestamp.Before(from) {
			continue
		}
		if !to.IsZero() && sm.Timestamp.After(to) {
			continue
		}
		out = append(out, sm)
	}
	return out
}

// ErrNotFound is returned when a hedge is not present in the store.
var ErrNotFound = errNotFound{}

type errNotFound struct{}

func (errNotFound) Error() string { return "hedge not found" }

// cloneHedge returns a deep copy of h safe to return to callers.
func cloneHedge(h *domain.Hedge) *domain.Hedge {
	out := *h
	if h.Fills != nil {
		out.Fills = append([]domain.Fill(nil), h.Fills...)
	}
	return &out
}