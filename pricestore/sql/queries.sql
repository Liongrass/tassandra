-- name: InsertExchangePrice :exec
INSERT INTO exchange_prices (exchange, currency, value, minute_ts)
VALUES (?, ?, ?, ?)
ON CONFLICT(exchange, currency, minute_ts) DO UPDATE SET value = excluded.value;

-- name: InsertAggregatedPrice :exec
INSERT INTO aggregated_prices (currency, value, minute_ts)
VALUES (?, ?, ?)
ON CONFLICT(currency, minute_ts) DO UPDATE SET value = excluded.value;

-- name: LatestAggregatedPrice :one
SELECT id, currency, value, minute_ts
FROM aggregated_prices
WHERE currency = ?
ORDER BY minute_ts DESC
LIMIT 1;

-- name: AggregatedPriceAt :one
SELECT id, currency, value, minute_ts
FROM aggregated_prices
WHERE currency = ? AND minute_ts <= ?
ORDER BY minute_ts DESC
LIMIT 1;

-- name: LatestExchangePrice :one
SELECT id, exchange, currency, value, minute_ts
FROM exchange_prices
WHERE currency = ? AND exchange = ?
ORDER BY minute_ts DESC
LIMIT 1;

-- name: ExchangePriceAt :one
SELECT id, exchange, currency, value, minute_ts
FROM exchange_prices
WHERE currency = ? AND exchange = ? AND minute_ts = ?;

-- name: ListAggregatedPrices :many
SELECT id, currency, value, minute_ts
FROM aggregated_prices
WHERE currency = ? AND minute_ts >= ? AND minute_ts <= ?
ORDER BY minute_ts ASC;
