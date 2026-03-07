CREATE TABLE IF NOT EXISTS exchange_prices (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    exchange    TEXT    NOT NULL,
    currency    TEXT    NOT NULL,
    value       INTEGER NOT NULL,
    minute_ts   INTEGER NOT NULL,
    UNIQUE(exchange, currency, minute_ts)
);

CREATE TABLE IF NOT EXISTS aggregated_prices (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    currency    TEXT    NOT NULL,
    value       INTEGER NOT NULL,
    minute_ts   INTEGER NOT NULL,
    UNIQUE(currency, minute_ts)
);

CREATE INDEX IF NOT EXISTS idx_exchange_prices_lookup
    ON exchange_prices(currency, exchange, minute_ts);

CREATE INDEX IF NOT EXISTS idx_aggregated_prices_lookup
    ON aggregated_prices(currency, minute_ts);
