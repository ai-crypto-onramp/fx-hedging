-- Conventions: UUID PKs (app-generated UUIDv7, no DB default), UPPER_CASE enum
-- TEXT (no CHECK), created_at + updated_at on every table, no DB triggers.

-- fx_exposures: per-currency net exposure snapshots (append-mostly).
CREATE TABLE IF NOT EXISTS fx_exposures (
    id              UUID PRIMARY KEY,
    currency        TEXT         NOT NULL,
    net_amount      DOUBLE PRECISION NOT NULL DEFAULT 0,
    hedge_coverage  DOUBLE PRECISION NOT NULL DEFAULT 0,
    open_amount     DOUBLE PRECISION NOT NULL DEFAULT 0,
    source_flow     TEXT         NOT NULL DEFAULT '',
    event_id        TEXT         NOT NULL DEFAULT '',
    ts              TIMESTAMPTZ  NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS fx_exposures_currency_ts_idx ON fx_exposures (currency, ts);

-- hedges: hedge records with currency, notional, tenor, type, status, policy context.
CREATE TABLE IF NOT EXISTS hedges (
    id                UUID PRIMARY KEY,
    currency          TEXT         NOT NULL,
    notional          DOUBLE PRECISION NOT NULL,
    tenor             TEXT         NOT NULL,
    type              TEXT         NOT NULL,
    status            TEXT         NOT NULL,
    quoted_rate       DOUBLE PRECISION NOT NULL DEFAULT 0,
    slippage_bps      DOUBLE PRECISION NOT NULL DEFAULT 0,
    pnl               DOUBLE PRECISION NOT NULL DEFAULT 0,
    client_request_id TEXT         NOT NULL DEFAULT '',
    policy_ratio      DOUBLE PRECISION NOT NULL DEFAULT 0,
    policy_cap_usd    DOUBLE PRECISION NOT NULL DEFAULT 0,
    cap_breached      BOOLEAN      NOT NULL DEFAULT false,
    value_date        DATE,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS hedges_status_idx ON hedges (status);
CREATE INDEX IF NOT EXISTS hedges_client_request_id_idx ON hedges (client_request_id);

-- hedge_executions: per-fill details linked to a hedge.
CREATE TABLE IF NOT EXISTS hedge_executions (
    id              UUID PRIMARY KEY,
    hedge_id        UUID         NOT NULL,
    venue           TEXT         NOT NULL,
    venue_trade_id   TEXT         NOT NULL,
    fill_price      DOUBLE PRECISION NOT NULL,
    quoted_price    DOUBLE PRECISION NOT NULL,
    slippage_bps    DOUBLE PRECISION NOT NULL DEFAULT 0,
    amount          DOUBLE PRECISION NOT NULL,
    ts              TIMESTAMPTZ  NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    UNIQUE (venue, venue_trade_id)
);
CREATE INDEX IF NOT EXISTS hedge_executions_hedge_venue_trade_idx ON hedge_executions (hedge_id, venue_trade_id);

-- fx_pnl: realized + unrealized P&L entries with attribution.
CREATE TABLE IF NOT EXISTS fx_pnl (
    id              UUID PRIMARY KEY,
    hedge_id        UUID,
    currency        TEXT         NOT NULL,
    component       TEXT         NOT NULL,
    realized        DOUBLE PRECISION NOT NULL DEFAULT 0,
    unrealized      DOUBLE PRECISION NOT NULL DEFAULT 0,
    rate            DOUBLE PRECISION NOT NULL DEFAULT 0,
    ts              TIMESTAMPTZ  NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS fx_pnl_currency_ts_idx ON fx_pnl (currency, ts);

-- slippage_samples: quoted vs achieved rate samples per execution.
CREATE TABLE IF NOT EXISTS slippage_samples (
    id              UUID PRIMARY KEY,
    pair            TEXT         NOT NULL,
    hedge_id        UUID,
    execution_id    BIGINT,
    quoted_rate     DOUBLE PRECISION NOT NULL,
    executed_rate   DOUBLE PRECISION NOT NULL,
    slippage_bps    DOUBLE PRECISION NOT NULL DEFAULT 0,
    ts              TIMESTAMPTZ  NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS slippage_samples_pair_ts_idx ON slippage_samples (pair, ts);