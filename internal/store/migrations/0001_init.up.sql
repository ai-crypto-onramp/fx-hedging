CREATE TABLE IF NOT EXISTS fx_exposures (
    id          BIGSERIAL    PRIMARY KEY,
    currency    TEXT         NOT NULL,
    amount      NUMERIC(20, 8) NOT NULL,
    source_flow TEXT         NOT NULL,
    ts          TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_fx_exposures_currency_ts
    ON fx_exposures (currency, ts);

CREATE TABLE IF NOT EXISTS hedges (
    id              BIGSERIAL    PRIMARY KEY,
    currency        TEXT         NOT NULL,
    notional        NUMERIC(20, 8) NOT NULL,
    tenor           TEXT         NOT NULL,
    type            TEXT         NOT NULL,
    status          TEXT         NOT NULL DEFAULT 'submitted',
    quoted_rate     NUMERIC(18, 10),
    policy_ratio    NUMERIC(6, 4),
    policy_cap_usd  NUMERIC(18, 2),
    client_request_id TEXT      NOT NULL,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT hedges_status_check
        CHECK (status IN ('submitted', 'partial', 'filled', 'rejected', 'cancelled')),
    CONSTRAINT hedges_type_check
        CHECK (type IN ('spot', 'forward'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_hedges_client_request_id
    ON hedges (client_request_id);

CREATE INDEX IF NOT EXISTS idx_hedges_status
    ON hedges (status);

CREATE INDEX IF NOT EXISTS idx_hedges_currency_created_at
    ON hedges (currency, created_at);

CREATE TABLE IF NOT EXISTS hedge_executions (
    id             BIGSERIAL    PRIMARY KEY,
    hedge_id       BIGINT       NOT NULL,
    venue          TEXT         NOT NULL,
    venue_trade_id TEXT         NOT NULL,
    fill_price     NUMERIC(18, 10) NOT NULL,
    fill_amount    NUMERIC(20, 8) NOT NULL,
    quoted_price   NUMERIC(18, 10),
    slippage_bps   NUMERIC(10, 4),
    status         TEXT         NOT NULL DEFAULT 'filled',
    executed_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT hedge_executions_hedge_fk
        FOREIGN KEY (hedge_id) REFERENCES hedges (id) ON DELETE CASCADE,
    CONSTRAINT hedge_executions_status_check
        CHECK (status IN ('filled', 'partial', 'rejected'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_hedge_executions_hedge_venue_trade
    ON hedge_executions (hedge_id, venue_trade_id);

CREATE INDEX IF NOT EXISTS idx_hedge_executions_hedge_id
    ON hedge_executions (hedge_id);

CREATE TABLE IF NOT EXISTS fx_pnl (
    id          BIGSERIAL    PRIMARY KEY,
    currency    TEXT         NOT NULL,
    hedge_id    BIGINT,
    component   TEXT         NOT NULL,
    amount      NUMERIC(20, 8) NOT NULL,
    rate        NUMERIC(18, 10),
    ts          TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT fx_pnl_component_check
        CHECK (component IN ('revaluation', 'slippage', 'realized')),
    CONSTRAINT fx_pnl_hedge_fk
        FOREIGN KEY (hedge_id) REFERENCES hedges (id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_fx_pnl_currency_ts
    ON fx_pnl (currency, ts);

CREATE INDEX IF NOT EXISTS idx_fx_pnl_hedge_id
    ON fx_pnl (hedge_id);

CREATE TABLE IF NOT EXISTS slippage_samples (
    id             BIGSERIAL    PRIMARY KEY,
    hedge_id       BIGINT       NOT NULL,
    execution_id   BIGINT       NOT NULL,
    currency_pair  TEXT         NOT NULL,
    quoted_rate    NUMERIC(18, 10) NOT NULL,
    fill_rate      NUMERIC(18, 10) NOT NULL,
    slippage_bps   NUMERIC(10, 4) NOT NULL,
    ts             TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT slippage_samples_hedge_fk
        FOREIGN KEY (hedge_id) REFERENCES hedges (id) ON DELETE CASCADE,
    CONSTRAINT slippage_samples_execution_fk
        FOREIGN KEY (execution_id) REFERENCES hedge_executions (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_slippage_samples_pair_ts
    ON slippage_samples (currency_pair, ts);

CREATE INDEX IF NOT EXISTS idx_slippage_samples_hedge_id
    ON slippage_samples (hedge_id);