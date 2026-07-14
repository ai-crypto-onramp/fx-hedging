# Project Plan — FX & Hedging

Implementation plan for the FX & Hedging service, the fiat-side risk management
component of the crypto on-ramp. The service aggregates per-currency net
exposure from flows, decides and executes hedges (spot / forward) via bank and
FX venues, tracks slippage vs quoted rates, and produces P&L attribution and
settlement netting views. Stages are ordered so that each builds on data and
behavior produced by the prior stage.

## Stage 1: Database schema for exposures, hedges, executions, P&L, slippage

### Goal
Establish the PostgreSQL append-mostly schema that all later stages depend on,
covering the five core tables described in the README data model.

### Tasks
- [x] Initialize Go module and project layout (`cmd/fx-hedging`, `internal/...`).
- [ ] Add PostgreSQL driver and migration runner (e.g. `golang-migrate`).
- [ ] Create migration `0001_init.sql` with tables: `fx_exposures`, `hedges`,
      `hedge_executions`, `fx_pnl`, `slippage_samples`.
- [ ] Define indexes on `(currency, ts)`, `hedges(status)`, and
      `hedge_executions(hedge_id, venue_trade_id)` for idempotency lookups.
- [x] Add `internal/store` package with typed repository structs and context-aware
      query helpers for each table.
- [x] Add config loading (`DB_URL`, `PORT`, `GRPC_PORT`, etc.) from env vars.
- [x] Add a basic `main.go` that connects to the DB on startup and runs ping.

### Acceptance criteria
- `go build ./...` and `go vet ./...` pass.
- `migrate up` creates all five tables in a clean DB; `migrate down` reverses.
- Repository packages compile and are covered by unit tests using a test DB or
  transaction-rolled-back harness.
- README "Local Development" commands run without modification.

## Stage 2: Per-currency exposure tracking from flows

### Goal
Build the in-memory + persisted exposure aggregation engine that consumes flow
events from Payment Orchestration, Treasury, and Ledger, and maintains a signed
net position per currency updated within 2 seconds of a settled flow.

### Tasks
- [x] Define an exposure event ingest interface (REST or gRPC stream ingress).
- [x] Implement in-memory aggregation keyed by currency with signed deltas.
- [ ] Persist snapshots to `fx_exposures` on each change and on a configurable
      `EXPOSURE_REFRESH_INTERVAL_MS` tick.
- [x] Expose `GET /v1/exposure/{currency}` returning net exposure, hedge
      coverage, and open (unhedged) amount.
- [ ] Implement `GetNetExposure(currency)` and `StreamExposure(currency)` gRPC
      service stubs.
- [ ] Add idempotency on event id to prevent double counting on replay.
- [x] Unit + integration tests for long/short netting and snapshot replay.

### Acceptance criteria
- A settled flow event updates `GET /v1/exposure/{currency}` within 2s.
- Replaying the event stream from `fx_exposures` reproduces the current net.
- Duplicate event ids do not change the net position.
- gRPC `StreamExposure` pushes updates on each snapshot.

## Stage 3: Hedge policy (ratio target + max open exposure cap)

### Goal
Implement the configurable hedge policy layer that decides how much of net
exposure to hedge per currency and enforces a hard USD-equivalent cap on open
(unhedged) exposure.

### Tasks
- [x] Load `HEDGE_RATIO_TARGET` and `MAX_OPEN_EXPOSURE_USD` from config.
- [ ] Support per-currency overrides (ratio and cap) for EM / low-liquidity CCYs.
- [x] Implement `policy.Decide(currency, netExposure) -> HedgeDecision` returning
      target notional, tenor hint, and whether the decision is blocked by the cap.
- [ ] Emit an alertable event when `MAX_OPEN_EXPOSURE_USD` is breached and block
      new flow that would increase it.
- [ ] Persist policy context (ratio used, cap state) on each hedge record.
- [x] Unit tests covering: under target, at target, cap breach, override.

### Acceptance criteria
- Decision returns the correct target notional for `0.90` ratio on a sample net.
- Cap breach blocks new exposure-increasing flow and emits an event.
- Per-currency overrides take precedence over the default ratio/cap.
- Policy context is retrievable from the `hedges` row.

## Stage 4: Hedge execution via bank FX API and external venues

### Goal
Implement the common execution interface and two adapters (bank FX API REST +
external FX venue) that submit spot and forward hedges and track fills.

### Tasks
- [x] Define `Executor` interface: `Quote`, `Submit`, `Cancel`, fill callbacks.
- [ ] Implement bank FX API adapter (REST) using `BANK_API_URL` / `BANK_API_KEY`.
- [ ] Implement external FX venue adapter using `FX_VENUE_URL` / `FX_VENUE_API_KEY`.
- [x] Add spot (T+2) and forward (dated tenor) order construction.
- [ ] Implement multi-venue routing: select by price/liquidity/cost, support
      execution splits and per-venue fill tracking.
- [x] Persist hedge and fill rows to `hedges` / `hedge_executions` with status
      transitions (submitted -> partial -> filled / rejected).
- [ ] Enforce idempotency on client request id + venue trade id.
- [x] Expose `POST /v1/hedges` and `GET /v1/hedges/:id` REST endpoints.
- [ ] Implement `SubmitHedgePlan(...)` gRPC handler for batched Treasury hedges.
- [ ] Decision-to-fill latency target < 500 ms for spot orders.

### Acceptance criteria
- A `POST /v1/hedges` spot order is filled end-to-end in < 500 ms in a sandbox.
- Duplicate submission with same client request id returns the original hedge.
- Multi-venue split records per-venue fills with separate trade ids.
- `GET /v1/hedges/:id` returns status, fills, slippage, and P&L fields.

## Stage 5: Slippage tracking vs quoted rate

### Goal
Capture quoted rate at decision time and achieved fill rate for every hedge
execution, persist slippage samples, and surface aggregates for reporting and
policy feedback.

### Tasks
- [x] Capture and persist the quoted rate alongside each hedge at decision time.
- [x] On each fill, compute `slippage = fill_rate - quoted_rate` (in pips/bps).
- [x] Write rows to `slippage_samples` and link to `hedge_executions`.
- [x] Expose `GET /v1/slippage?pair=&from=&to=` returning samples + aggregates.
- [ ] Trigger an alert when slippage exceeds `SLIPPAGE_ALERT_BPS`.
- [ ] Feed aggregate slippage back into policy tuning (e.g. widen ratio for
      high-slippage currencies).

### Acceptance criteria
- Every fill has a corresponding `slippage_samples` row with quoted + fill rate.
- `GET /v1/slippage` returns per-pair aggregates (mean, max, count) over a range.
- Slippage above the configured bps threshold raises an alert event.
- Slippage is visible on `GET /v1/hedges/:id`.

## Stage 6: P&L attribution and settlement netting

### Goal
Produce realized and unrealized P&L per currency and per hedge, separating FX
revaluation P&L from execution slippage cost, and net offsetting settlement
obligations per currency to minimize cash movement.

### Tasks
- [x] Implement FX revaluation P&L using current live rate vs hedge book rate.
- [x] Implement realized P&L on hedge close / settlement.
- [x] Attribute P&L entries to `fx_pnl` with component tags
      (`revaluation`, `slippage`).
- [x] Expose `GET /v1/pnl?from=&to=` with attribution by currency and component.
- [ ] Implement settlement netting engine per currency across flows + hedges.
- [ ] Emit netted settlement obligations to Reconciliation for T+1 matching.
- [x] Unit + integration tests for revaluation, realized, and netting.

### Acceptance criteria
- `GET /v1/pnl` returns realized + unrealized P&L split by component.
- Revaluation P&L moves with live rate changes; slippage P&L is fixed at fill.
- Netting reduces offsetting obligations to a single net cash movement per CCY.
- Netted obligations are consumable by Reconciliation.

## Stage 7: Pricing / Quote live FX integration

### Goal
Serve the `GetLiveRate(currency)` gRPC method used by Pricing / Quote to feed
quote spreads, backed by real-time FX rates sourced from execution venues.

### Tasks
- [ ] Implement a rate cache fed by venue quote streams and bank API quotes.
- [ ] Implement `GetLiveRate(currency)` gRPC handler returning cached + fresh rate.
- [ ] Add staleness guard: refuse to serve rates older than a configurable TTL.
- [ ] Cross-check live rate against the rate used for revaluation P&L.
- [ ] gRPC + unit tests for cache hit, stale, and refresh paths.

### Acceptance criteria
- `GetLiveRate` returns a rate within TTL of the latest venue quote.
- Stale rates are rejected with a clear error so callers fall back.
- Live rate is consistent with the revaluation rate used in Stage 6.

## Stage 8: Treasury Orchestration integration

### Goal
Wire the inbound Treasury Orchestration integration so Treasury can query
aggregate exposure and submit batched / forward hedge plans covering the
T+0 vs T+2/3 float.

### Tasks
- [ ] Finalize `GetNetExposure`, `StreamExposure`, `SubmitHedgePlan` gRPC service.
- [ ] Map Treasury float tenor requests to forward contracts in Stage 4.
- [ ] Return batched hedge results (per-hedge status, fills, slippage) to Treasury.
- [ ] Coordinate with policy layer to reject plans that breach the open cap.
- [ ] Integration tests with a Treasury Orchestration stub client.

### Acceptance criteria
- Treasury can stream exposure updates and receive them in order.
- `SubmitHedgePlan` executes each leg and returns aggregated results.
- Plans that would breach `MAX_OPEN_EXPOSURE_USD` are rejected with a reason.

## Stage 9: Audit emission and reconciliation feed

### Goal
Emit every exposure snapshot, hedge decision, execution, and P&L entry to the
audit-event-log, and feed hedge executions + settlement obligations to
Reconciliation for T+1 matching.

### Tasks
- [ ] Add `audit-event-log` gRPC client using `AUDIT_EVENT_LOG_URL`.
- [x] Emit audit events on: snapshot, hedge decision, submission, fill, P&L entry.
- [ ] Ensure events are idempotent and ordered per entity.
- [ ] Publish execution records and netted settlement obligations to
      Reconciliation on T+1.
- [ ] Add retry + dead-letter handling for audit/recon delivery.
- [ ] Tests verify event payload shape and ordering guarantees.

### Acceptance criteria
- Every state change produces exactly one audit event (no duplicates on retry).
- Reconciliation receives hedge executions and netted obligations for T+1.
- Failed delivery retries and eventually lands in a dead-letter store.

## Stage 10: Tests, coverage, and Docker / CI

### Goal
Harden the service for production: comprehensive tests, CI workflow,
and a containerized build matching the repo's CI badge.

### Tasks
- [x] Raise unit + integration tests; report coverage in CI.
- [ ] Add `Dockerfile` (multi-stage Go build) and `docker-compose.yml` with
      PostgreSQL + the service for local integration.
- [x] Add `.github/workflows/ci.yml` running `go vet`, `go test -race`, coverage
      upload to Codecov, and Docker build.
- [ ] Add load test simulating flow burst to validate < 2s exposure latency and
      < 500 ms spot execution latency.
- [ ] Add failure-mode tests: venue down (fail safe, no cap breach growth),
      DB outage (replay on recovery), duplicate callbacks.
- [ ] Update README with any new run instructions.

### Acceptance criteria
- CI badge green on `main`; coverage reported to Codecov.
- `docker compose up` brings up the service + DB and serves health endpoints.
- Load test confirms latency SLOs under burst.
- Fail-safe behavior verified for venue and DB outages.