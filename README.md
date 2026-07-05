# FX & Hedging

![CI](https://github.com/ai-crypto-onramp/fx-hedging/actions/workflows/ci.yml/badge.svg)
[![codecov](https://codecov.io/gh/ai-crypto-onramp/fx-hedging/branch/main/graph/badge.svg)](https://codecov.io/gh/ai-crypto-onramp/fx-hedging)

Manages currency exposure across daily flows, executes hedges, and tracks slippage for the crypto on-ramp.

## Overview / Responsibilities

The FX & Hedging service is the fiat-side risk management component of the on-ramp. It
continuously aggregates per-currency net exposure produced by user flows (payments,
crypto buys, settlements), decides when and how much to hedge, executes that hedge
across bank APIs and FX venues (spot or forward), and accounts for the resulting P&L
and slippage against quoted rates.

Per the platform architecture, this service sits in the **Fiat, Pricing & Liquidity**
layer. It is called synchronously by **Pricing / Quote** for live FX rate inputs, and
by **Treasury Orchestration** for aggregate hedge decisions covering the T+0 vs T+2/3
float. It executes through **Exchange Connectors** and OTC/bank desks, and reports
into the **Audit / Event Log** and **Reconciliation** services.

Core responsibilities:

- Track per-currency net exposure from aggregate daily flows.
- Execute hedges (spot / forward) to neutralize directional exposure.
- Measure and record slippage vs the quoted rate at execution time.
- Enforce hedge ratio policy and open-exposure limits.
- Produce P&L attribution and settlement netting views.

## Language & Tech Stack

- **Language:** Go (transactional backbone — concurrency, latency, ops maturity).
- **Exposure calculation engine:** in-memory aggregation over the event stream from
  Payment Orchestration, Treasury, and Ledger, persisted to PostgreSQL for replay.
- **Forward contract execution:** via bank FX APIs (REST/ FIX) and external FX
  venues, abstracted behind a common execution interface so new venues/desks can be
  added without changing the exposure or P&L logic.
- **Persistence:** PostgreSQL (append-mostly ledger-style tables for exposures,
  hedges, executions, P&L, slippage).
- **Transport:** REST for operator/internal queries; gRPC for inter-service calls
  from Pricing / Quote and Treasury Orchestration.

## System Requirements

1. **Per-currency net exposure tracking.** Maintain a real-time running net position
   per currency derived from aggregate user flows (settled payments, crypto buys,
   refunds, settlements). Exposure is currency-specific and signed (long/short).
2. **Hedge execution (spot / forward).** When net exposure in a currency breaches
   policy thresholds, execute hedges to neutralize the directional risk. Support
   both **spot** trades (T+2 settlement) and **forward contracts** (dated tenor) to
   match the T+0 vs T+2/3 funding profile of the on-ramp.
3. **Slippage tracking vs quoted rate.** For every hedge execution, capture the
   quoted rate at decision time and the achieved fill rate, record the difference as
   slippage, and aggregate it for reporting and policy feedback.
4. **Hedge ratio policy.** Enforce a configurable target hedge ratio per currency
   (e.g. hedge 90% of net exposure, leave 10% uncovered for tactical management)
   and a hard cap on maximum open (unhedged) exposure in USD-equivalent.
5. **P&L attribution.** Attribute realized and unrealized P&L per currency and per
   hedge, separating FX revaluation P&L from execution slippage cost, suitable for
   feeding the Ledger and finance reporting.
6. **Settlement netting.** Net offsetting settlement obligations across flows and
   hedges per currency to minimize actual cash movement to/from bank accounts.
7. **Multi-venue execution.** Route hedge orders across the bank FX API and external
   FX venues, selecting by price/liquidity/cost, with execution splits and fill
   tracking per venue.

## Non-Functional Requirements

- **Exposure calculation latency:** near-real-time, with exposure figures updated
  within **< 2 seconds** of a contributing flow event settling.
- **Hedge execution latency target:** end-to-end decision-to-fill **< 500 ms** for
  spot orders submitted to a venue API (excluding venue-side settlement time).
- **Availability:** 99.95% during fiat operating hours; degradation must fail safe
  (no unhedged exposure growth beyond policy cap while execution is unavailable).
- **Auditability:** every exposure snapshot, hedge decision, execution, and P&L
  attribution entry is persisted and emitted to the audit-event-log for compliance.
- **P&L reporting:** daily and intraday P&L reports queryable on demand; T+1
  reconciliation against bank statements and venue fill reports.
- **Idempotency:** all hedge submissions and execution callbacks are idempotent on
  client request id + venue trade id.

## Technical Specifications

### API Surface

- **REST** — operator-facing queries and ad-hoc hedge requests (internal network).
- **Internal gRPC** — high-throughput calls from Pricing / Quote (live FX) and
  Treasury Orchestration (aggregate hedge needs).

### Endpoints (REST)

| Method | Path | Description |
|---|---|---|
| `GET` | `/v1/exposure/{currency}` | Current net exposure, hedge coverage, and open (unhedged) amount for a currency. |
| `POST` | `/v1/hedges` | Request a new hedge. Body: `{ "currency": "EUR", "notional": 250000, "tenor": "spot", "type": "spot" }`. |
| `GET` | `/v1/hedges/:id` | Status, fills, slippage, and P&L for a specific hedge. |
| `GET` | `/v1/pnl?from=&to=` | Realized + unrealized P&L, attribution by currency and component, over a date range. |
| `GET` | `/v1/slippage?pair=&from=&to=` | Slippage samples and aggregates for a currency pair over a date range. |

### gRPC Services

- `GetLiveRate(currency)` — called by Pricing / Quote.
- `GetNetExposure(currency)` / `StreamExposure(currency)` — called by Treasury.
- `SubmitHedgePlan(...)` — called by Treasury Orchestration for batched hedges.

### Data Model (PostgreSQL)

| Table | Purpose |
|---|---|
| `fx_exposures` | Per-currency net exposure snapshots (signed amount, source flow, timestamp). Append-mostly. |
| `hedges` | Hedge records: currency, notional, tenor, type (spot/forward), status, policy context. |
| `hedge_executions` | Per-fill details: venue, fill price, quoted price, slippage, venue trade id. |
| `fx_pnl` | Realized and unrealized P&L entries with attribution (revaluation vs slippage). |
| `slippage_samples` | Quoted vs achieved rate samples per execution, for analytics and policy tuning. |

### Hedge Policy

- `HEDGE_RATIO_TARGET` — target fraction of net exposure to hedge per currency
  (e.g. `0.90`). Applied per-currency; overrides available for high-volatility
  currencies.
- `MAX_OPEN_EXPOSURE_USD` — hard cap on unhedged net exposure in USD-equivalent
  across all currencies. Breaching this cap is an alertable incident and blocks new
  flow that would increase it.
- Per-currency overrides: distinct ratio and cap for emerging-market or low-liquidity
  currencies.

### Integrations

| Direction | Service | Purpose |
|---|---|---|
| Inbound (sync) | **Pricing / Quote** | Calls FX for live FX rates feeding quote spreads. |
| Inbound (sync/async) | **Treasury Orchestration** | Sends aggregate hedge needs for batched/forward hedging against the float. |
| Outbound | **Exchange Connectors / OTC desks / bank FX API** | Hedge execution and fill reporting. |
| Outbound (async) | **Audit / Event Log** | Emits exposure snapshots, hedge decisions, executions, P&L entries. |
| Outbound (async) | **Reconciliation** | Feeds hedge executions and settlement obligations for T+1 matching. |

## Dependencies

- **PostgreSQL** — persistent store for exposures, hedges, executions, P&L, slippage.
- **FX execution venues / bank FX API** — spot and forward execution; REST and/or
  FIX adapters behind the common execution interface.
- **audit-event-log** — append-only audit trail consumer for compliance/forensics.
- **Pricing / Quote** and **Treasury Orchestration** — upstream callers (gRPC).
- **Reconciliation** — downstream consumer of execution and settlement records.

## Configuration

Configuration is via environment variables.

| Variable | Description | Example |
|---|---|---|
| `PORT` | REST listen port. | `8080` |
| `GRPC_PORT` | Internal gRPC listen port. | `9090` |
| `DB_URL` | PostgreSQL connection string. | `postgres://fx:pwd@db:5432/fx?sslmode=require` |
| `HEDGE_RATIO_TARGET` | Default target hedge ratio per currency. | `0.90` |
| `MAX_OPEN_EXPOSURE_USD` | Hard cap on unhedged USD-equivalent exposure. | `500000` |
| `BANK_API_URL` | Bank FX API base URL. | `https://fx.bank.example.com` |
| `BANK_API_KEY` | Bank FX API credential (secret). | `••••` |
| `FX_VENUE_URL` | External FX venue base URL. | `https://api.fxvenue.example.com` |
| `FX_VENUE_API_KEY` | External FX venue credential (secret). | `••••` |
| `AUDIT_EVENT_LOG_URL` | audit-event-log gRPC endpoint. | `audit:9090` |
| `EXPOSURE_REFRESH_INTERVAL_MS` | Poll/aggregate interval for exposure engine. | `1000` |
| `SLIPPAGE_ALERT_BPS` | Slippage threshold (basis points) for alerting. | `5` |
| `LOG_LEVEL` | Log level (`debug`/`info`/`warn`/`error`). | `info` |

## Local Development

```bash
# Build
go build ./...

# Run (requires PostgreSQL + reachable FX venue/bank sandbox)
go run ./cmd/fx-hedging

# Tests
go test ./...

# Lint / vet
go vet ./...
```
