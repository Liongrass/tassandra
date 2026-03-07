# Tassandra — Project Plan

## Architecture Overview

```
tassandra/
├── cmd/
│   ├── tassandra/          # daemon entry point
│   │   └── main.go
│   └── tassandra-cli/      # CLI client
│       └── main.go
├── proto/
│   └── tassandrarpc/
│       └── tassandra.proto
├── tassandrarpc/           # generated protobuf + gRPC stubs
├── pricefeed/              # exchange adapters
│   ├── interface.go
│   ├── binance.go
│   ├── kraken.go
│   ├── coinbase.go
│   └── bitstamp.go
├── pricestore/             # per-minute storage
│   ├── interface.go
│   └── sqlite.go
├── oracle/                 # aggregation logic
│   └── oracle.go
├── rpcserver.go            # gRPC server impl
├── httpserver.go           # HTTP endpoint (behind Aperture)
├── config.go               # config structs
├── tassandra.conf          # default config file
├── log.go
├── server.go               # main daemon wiring
├── go.mod
└── go.sum
```

---

## Key Components

### 1. Price Feed Interface (`pricefeed/interface.go`)
Each exchange adapter implements a common interface:
```go
type PriceFeed interface {
    // FetchPrice fetches the current BTC price in the given fiat currency.
    FetchPrice(ctx context.Context, currency FiatCurrency) (Price, error)
    // Name returns the exchange name.
    Name() string
}
```
Initial exchange support: Binance, Kraken, Coinbase, Bitstamp.

### 2. Price Store (`pricestore/`)
- Uses **sqlite** (Lightning Labs standard) for embedded, persistent storage
- Schema: bucket per currency, key = unix minute timestamp, value = aggregated price + per-exchange breakdown
- Interface-driven for testability

### 3. Oracle (`oracle/`)
- Polls all configured feeds every 60 seconds
- Aggregation strategy: **median** (robust against outliers/manipulation)
- Markup is **per-asset**, configured in ppm via `[Asset]` stanzas (see §7)
- Persists each sample to the store
- Make gRPC interface compatible with tapd RFQ system (https://github.com/lightninglabs/taproot-assets/tree/main/rfq)

### 4. gRPC Server (`rpcserver.go`)
Intended for Taproot Asset edge nodes that need marked-up prices for RFQ
quoting. Implements the `PriceOracle` service defined in tapd's
`taprpc/priceoraclerpc/price_oracle.proto`, making Tassandra a drop-in
external price oracle for any tapd node.

Single RPC method:
- `QueryAssetRates(QueryAssetRatesRequest) → QueryAssetRatesResponse`
  - Accepts: transaction type (PURCHASE/SALE), subject asset specifier,
    payment asset specifier, optional amount constraints, intent, and metadata
  - Returns: `AssetRates` containing subject and payment asset rates expressed
    as `FixedPoint` (coefficient + scale, no floating point), plus an
    expiration timestamp
  - Markup is applied at this layer before returning rates
  - Asset specifier (asset ID or group key) is mapped to a fiat currency via
    configuration

Tassandra will vendor or import `price_oracle.proto` directly from tapd rather
than defining its own proto, to guarantee interface compatibility.

### 5. HTTP Server (`httpserver.go`)
Intended for point-of-sale devices that need an up-to-date market price.
Returns **raw aggregated prices with no markup applied**.

Endpoints:
- `GET /price/{currency}` — current aggregated price
- `GET /price/{currency}?exchange={name}&date={unix_ts}` — historical price
  for a specific exchange and minute-aligned timestamp
- `GET /price/{currency}?date={unix_ts}` — historical aggregated price for a
  given timestamp

Sits behind **Aperture** L402 proxy — Tassandra itself doesn't handle macaroon
issuance, Aperture does. Tassandra exposes a plain HTTP port; Aperture
reverse-proxies it and enforces payment.

### 6. CLI (`cmd/tassandracli/`)
Uses `cobra` for subcommands:
- `tassandracli getprice --currency USD`
- `tassandracli subscribe --currency EUR`
- `tassandracli history --currency USD --start ... --end ...`
- `tassandracli listcurrencies`

### 7. Configuration (`config.go` + `tassandra.conf`)
Uses `go-flags` (Lightning Labs standard, ini-style):
```ini
[Application Options]
grpclisten=0.0.0.0:10590
httplisten=0.0.0.0:10591
loglevel=info

[Exchange]
binance.enabled=true
kraken.enabled=true
coinbase.enabled=true

[DB]
dbpath=~/.tassandra/tassandra.db

; One [Asset] section per Taproot Asset. The asset_id is the hex-encoded
; asset ID or group key. currency must match a supported fiat ticker.
; markup is expressed in parts-per-million (ppm).
[Asset "0a1b2c..."]
currency=USD
markup=5000

[Asset "deadbeef..."]
currency=EUR
markup=3000
```

Each `[Asset]` stanza is parsed into an `AssetConfig` struct:
```go
type AssetConfig struct {
    AssetID  string // hex asset ID or group key
    Currency FiatCurrency
    Markup   uint64 // ppm
}
```
The daemon builds a map of `AssetID → AssetConfig` at startup, used by the
gRPC server to look up the correct currency and apply the correct markup when
`QueryAssetRates` is called.

---

## Tech Stack & Lightning Labs Conventions

| Concern | Choice |
|---|---|
| Config | `go-flags` (ini-file + flags) |
| Logging | `btclog` |
| gRPC | `google.golang.org/grpc` + protobuf |
| Storage | `modernc.org/sqlite` + `sqlc` |
| CLI | `cobra` or raw `go-flags` subcommands |
| HTTP | `net/http` stdlib |
| L402 proxy | `github.com/lightninglabs/aperture` (external) |
| Error handling | sentinel errors + `fmt.Errorf("...: %w", err)` |
| Shutdown | `signal.NotifyContext` + `errgroup` |

---

## Data Flow

```
[Binance]──┐
[Kraken]───┤──► Oracle (median + markup) ──► sqlite store (per-minute)
[Coinbase]─┤         │
[Bitstamp]─┘         ├──► gRPC server ──► tassandracli / other consumers
                     └──► HTTP server ──► Aperture L402 ──► end users
```

---

## Phased Build Order

1. **Proto + codegen** — define gRPC API first
2. **`pricefeed/`** — implement exchange adapters + tests
3. **`pricestore/`** — sqlite schema + interface
4. **`oracle/`** — polling loop + aggregation + markup
5. **`rpcserver.go`** — wire oracle into gRPC
6. **`httpserver.go`** — simple JSON wrapper
7. **`cmd/tassandra/`** — daemon wiring, config, signals
8. **`cmd/tassandracli/`** — CLI client
9. **Aperture integration** — deployment-time config
