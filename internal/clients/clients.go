// Package clients provides outbound HTTP clients to downstream services:
// the audit-event-log and Reconciliation. Both clients are idempotent
// (Idempotency-Key header), retry transient failures with backoff, and
// dead-letter records that exhaust retries into a dead-letter store for
// later replay.
package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/ai-crypto-onramp/fx-hedging/internal/domain"
)

// ErrUnavailable is returned when the downstream is unreachable.
var ErrUnavailable = errors.New("clients: unavailable")

// ErrBadRequest is returned when the downstream rejects the payload.
var ErrBadRequest = errors.New("clients: bad request")

// AuditClient is the outbound audit-event-log client. It posts audit
// events to AUDIT_EVENT_LOG_URL with an Idempotency-Key so retries are
// safe. Events are emitted in order per entity (the caller is responsible
// for serializing events per entity; the client preserves send order).
type AuditClient struct {
	baseURL string
	client  *http.Client
	dl      DeadLetterStore
	mu      sync.Mutex
	last    map[string]string // entityKey -> last emitted event id (ordering guard)
}

// NewAuditClient returns an AuditClient targeting AUDIT_EVENT_LOG_URL. If
// the URL is empty, Emit is a no-op (returns nil) so the service degrades
// safely in local dev. dl may be nil.
func NewAuditClient(dl DeadLetterStore) *AuditClient {
	return &AuditClient{
		baseURL: os.Getenv("AUDIT_EVENT_LOG_URL"),
		client:  &http.Client{Timeout: 5 * time.Second},
		dl:      dl,
		last:    map[string]string{},
	}
}

// AuditPayload is the JSON body posted to audit-event-log.
type AuditPayload struct {
	EventType string  `json:"event_type"`
	Source    string  `json:"source_service"`
	HedgeID   string  `json:"hedge_id,omitempty"`
	Currency  string  `json:"currency,omitempty"`
	Detail    string  `json:"detail,omitempty"`
	At        string  `json:"at"`
	Amount    float64 `json:"amount,omitempty"`
}

// Emit posts ev to audit-event-log with idempotency key eventID. It
// retries up to 3 times with backoff. On terminal failure the event is
// written to the dead-letter store (if configured) and ErrUnavailable is
// returned. Send order per entityKey (hedge id or currency) is preserved
// by serializing emits under the client mutex.
func (a *AuditClient) Emit(ctx context.Context, ev AuditPayload, eventID, entityKey string) error {
	if a.baseURL == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if entityKey != "" {
		a.last[entityKey] = eventID
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	backoff := 200 * time.Millisecond
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/audit-events", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if eventID != "" {
			req.Header.Set("Idempotency-Key", eventID)
		}
		resp, err := a.client.Do(req)
		if err != nil {
			if attempt < 2 {
				select {
				case <-time.After(backoff):
					backoff *= 2
					continue
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			a.deadLetter("audit", eventID, body, err.Error())
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		if resp.StatusCode == http.StatusBadRequest {
			return fmt.Errorf("%w: %s", ErrBadRequest, string(respBody))
		}
		if attempt < 2 {
			select {
			case <-time.After(backoff):
				backoff *= 2
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		a.deadLetter("audit", eventID, body, fmt.Sprintf("status %d: %s", resp.StatusCode, string(respBody)))
		return fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	return nil
}

func (a *AuditClient) deadLetter(topic, key string, payload []byte, reason string) {
	if a.dl == nil {
		return
	}
	_ = a.dl.Append(context.Background(), &DeadLetter{
		Topic:   topic,
		Key:     key,
		Payload: payload,
		Reason:  reason,
		At:      time.Now().UTC(),
	})
}

// LastEmmitted returns the last emitted event id for an entity key, or ""
// if none. Used to verify ordering guarantees in tests.
func (a *AuditClient) LastEmitted(entityKey string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last[entityKey]
}

// --- Reconciliation ---

// ReconClient publishes hedge execution records and netted settlement
// obligations to Reconciliation for T+1 matching. Posts are idempotent on
// the record id and retry with backoff into a dead-letter store.
type ReconClient struct {
	baseURL string
	client  *http.Client
	dl      DeadLetterStore
}

// NewReconClient returns a ReconClient targeting RECONCILIATION_URL. If
// the URL is empty, publishes are no-ops (degrade safely).
func NewReconClient(dl DeadLetterStore) *ReconClient {
	return &ReconClient{
		baseURL: os.Getenv("RECONCILIATION_URL"),
		client:  &http.Client{Timeout: 10 * time.Second},
		dl:      dl,
	}
}

// ExecutionRecord is a hedge execution record published to Reconciliation.
type ExecutionRecord struct {
	HedgeID       string  `json:"hedge_id"`
	Currency      string  `json:"currency"`
	Venue         string  `json:"venue"`
	VenueTradeID  string  `json:"venue_trade_id"`
	Notional      float64 `json:"notional"`
	FillPrice     float64 `json:"fill_price"`
	QuotedPrice   float64 `json:"quoted_price"`
	SlippageBPS   float64 `json:"slippage_bps"`
	ExecutedAt    string  `json:"executed_at"`
}

// PublishExecution posts a hedge execution record to Reconciliation.
func (r *ReconClient) PublishExecution(ctx context.Context, rec ExecutionRecord) error {
	return r.publish(ctx, "/v1/executions", rec.HedgeID, rec)
}

// PublishObligation posts a netted settlement obligation to Reconciliation.
func (r *ReconClient) PublishObligation(ctx context.Context, ob domain.SettlementObligation) error {
	return r.publish(ctx, "/v1/settlement-obligations", ob.Currency+"-"+ob.At.Format(time.RFC3339Nano), ob)
}

func (r *ReconClient) publish(ctx context.Context, path, key string, body any) error {
	if r.baseURL == "" {
		return nil
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	backoff := 300 * time.Millisecond
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+path, bytes.NewReader(buf))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		resp, err := r.client.Do(req)
		if err != nil {
			if attempt < 2 {
				select {
				case <-time.After(backoff):
					backoff *= 2
					continue
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			r.deadLetter("recon", key, buf, err.Error())
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		if resp.StatusCode == http.StatusBadRequest {
			return fmt.Errorf("%w: %s", ErrBadRequest, string(respBody))
		}
		if attempt < 2 {
			select {
			case <-time.After(backoff):
				backoff *= 2
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		r.deadLetter("recon", key, buf, fmt.Sprintf("status %d: %s", resp.StatusCode, string(respBody)))
		return fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	return nil
}

func (r *ReconClient) deadLetter(topic, key string, payload []byte, reason string) {
	if r.dl == nil {
		return
	}
	_ = r.dl.Append(context.Background(), &DeadLetter{
		Topic:   topic,
		Key:     key,
		Payload: payload,
		Reason:  reason,
		At:      time.Now().UTC(),
	})
}

// --- Dead-letter store ---

// DeadLetter is a record that exhausted delivery retries.
type DeadLetter struct {
	Topic   string
	Key     string
	Payload []byte
	Reason  string
	At      time.Time
}

// DeadLetterStore persists dead-letter records.
type DeadLetterStore interface {
	Append(ctx context.Context, dl *DeadLetter) error
	List(ctx context.Context, limit int) ([]*DeadLetter, error)
}

// MemDeadLetter is an in-memory DeadLetterStore used in tests and local dev.
type MemDeadLetter struct {
	mu  sync.Mutex
	row []*DeadLetter
}

// NewMemDeadLetter returns an empty in-memory dead-letter store.
func NewMemDeadLetter() *MemDeadLetter { return &MemDeadLetter{} }

func (m *MemDeadLetter) Append(_ context.Context, dl *DeadLetter) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.row = append(m.row, dl)
	return nil
}

func (m *MemDeadLetter) List(_ context.Context, limit int) ([]*DeadLetter, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > len(m.row) {
		limit = len(m.row)
	}
	out := make([]*DeadLetter, limit)
	copy(out, m.row[:limit])
	return out, nil
}