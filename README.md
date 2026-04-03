# Tassandra

Tassandra is a Bitcoin price aggregator daemon written in Go. It polls multiple
exchanges every minute, computes a median price per fiat currency, and makes
prices available over two interfaces:

- **gRPC** — implements the [taproot-assets](https://github.com/lightninglabs/taproot-assets)
  `PriceOracle` service, making Tassandra a drop-in external price oracle for
  any `tapd` node running the RFQ subsystem. Prices are returned with a
  configurable per-asset markup.
- **HTTP** — a simple JSON endpoint for point-of-sale devices that need an
  up-to-date market price. No markup is applied. Designed to sit behind an
  [Aperture](https://github.com/lightninglabs/aperture) L402 proxy.

## Features

- Aggregates prices from **Binance**, **Kraken**, **Coinbase**, and **Bitstamp**
- Supports **USD**, **EUR**, and **GBP** (configurable)
- Stores per-exchange and aggregated prices at **one-minute resolution** in a
  local SQLite database
- Median aggregation — resistant to single-exchange outliers or manipulation
- Per-asset **PPM markup** on gRPC responses for tapd RFQ quoting
- Clean shutdown on SIGINT/SIGTERM

## Requirements

- Go 1.22 or later
- `make`
- `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc` — only needed to
  regenerate protobuf stubs (`make proto`)
- `sqlc` — only needed to regenerate SQL query code (`make sqlc`)

## Building

```bash
make build
```

This installs both binaries into `$GOPATH/bin` (typically `~/go/bin`):

| Binary | Description |
|---|---|
| `tassandra` | The daemon |
| `tassandra-cli` | The CLI client |

## Configuration

Copy the sample config and edit it:

```bash
mkdir -p ~/.tassandra
cp tassandra.conf.sample ~/.tassandra/tassandra.conf
```

The config file uses an ini-style format:

```ini
[Application Options]
grpclisten=127.0.0.1:10590
httplisten=127.0.0.1:10591
loglevel=info
; datadir=~/.tassandra

[Exchange]
; For each exchange, set a comma-separated list of fiat currencies to poll.
; Omit or comment out a line to disable that exchange.
binance=USD,EUR,GBP
kraken=USD,EUR,GBP
coinbase=USD
bitstamp=EUR,GBP

[Asset]
; One line per Taproot Asset served over gRPC.
; Format: <hex-asset-id>:<currency>:<markup-ppm>
; asset=0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20:USD:5000
; asset=a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2:EUR:3000
```

All options can also be passed as command-line flags (run `tassandra --help`
for the full list).

## Running

```bash
./bin/tassandra --loglevel=info
```

On first start Tassandra creates `~/.tassandra/` and immediately polls all
configured exchanges. Subsequent polls happen every 60 seconds.

## CLI

```bash
# Current aggregated price (no markup)
./bin/tassandra-cli getprice --currency USD

# Historical aggregated price
./bin/tassandra-cli getprice --currency USD --date 1709856000

# Historical price for a specific exchange
./bin/tassandra-cli getprice --currency USD --exchange kraken --date 1709856000

# Query the gRPC oracle for a Taproot Asset rate (with markup)
./bin/tassandra-cli getrate --asset <hex-asset-id>

# Query with a non-BTC payment asset and sale transaction type
./bin/tassandra-cli getrate --asset <hex> --payment-asset <hex> --type sale
```

Global flags:

| Flag | Default | Description |
|---|---|---|
| `--rpcserver` | `localhost:10590` | gRPC server address |
| `--httpserver` | `localhost:10591` | HTTP server address |

## HTTP API

All endpoints return JSON. No authentication is performed by Tassandra itself —
deploy behind [Aperture](https://github.com/lightninglabs/aperture) for L402
payment-gated access.

### `GET /price/{currency}`

Returns the current aggregated BTC price for the given fiat currency.

```bash
curl http://localhost:10591/price/USD
```

```json
{
  "currency": "USD",
  "price": "67874.00000000",
  "timestamp": 1709856000
}
```

### `GET /price/{currency}?date={unix_ts}`

Returns the aggregated price at or before the given minute-aligned Unix
timestamp.

```bash
curl http://localhost:10591/price/USD?date=1709856000
```

### `GET /price/{currency}?exchange={name}&date={unix_ts}`

Returns the raw price from a specific exchange at the given minute timestamp.

```bash
curl "http://localhost:10591/price/USD?exchange=kraken&date=1709856000"
```

```json
{
  "currency": "USD",
  "price": "67884.00000000",
  "timestamp": 1709856000,
  "exchange": "kraken"
}
```

## gRPC / tapd RFQ Integration

Tassandra implements the `PriceOracle` gRPC service defined in
[`taprpc/priceoraclerpc/price_oracle.proto`](https://github.com/lightninglabs/taproot-assets/blob/main/taprpc/priceoraclerpc/price_oracle.proto).

To connect a `tapd` node to Tassandra, set the following in your `tapd.conf`:

```ini
[Experimental]
experimental.rfq.priceoracleaddress=rfqrpc://localhost:10590
```

Each Taproot Asset that tapd should price must be registered in Tassandra's
`[Asset]` config section with its asset ID, fiat currency, and markup in PPM.

### Markup

The markup is applied only on the gRPC interface and not on the HTTP interface.
It is expressed in parts per million (PPM):

| PPM | Percentage |
|---|---|
| 1000 | 0.1% |
| 5000 | 0.5% |
| 10000 | 1.0% |

### FixedPoint rates

Rates are returned as `FixedPoint` values (integer coefficient + scale),
avoiding floating-point precision issues. The scale is always 8 for fiat
prices. When the payment asset is BTC, the payment rate is returned as
100,000,000,000 millisatoshis per BTC (scale 0).

## Development

```bash
make test          # run all tests
make vet           # run go vet
make proto         # regenerate gRPC stubs from proto/
make sqlc          # regenerate SQL query code from pricestore/sql/
make clean         # remove bin/
```

### Project layout

```
tassandra/          daemon entry point and config
tassandra-cli/      CLI client
oracle/             poll loop, median aggregation, subscriptions
pricefeed/          exchange adapters (Binance, Kraken, Coinbase, Bitstamp)
pricestore/         SQLite store and sqlc-generated query code
proto/              vendored price_oracle.proto from taproot-assets
tassandrarpc/       generated gRPC stubs
config.go           AssetConfig type
rpcserver.go        gRPC PriceOracle server implementation
httpserver.go       HTTP server
```

## Aperture / L402

Tassandra's HTTP server performs no authentication. To monetise access, deploy
[Aperture](https://github.com/lightninglabs/aperture) as a reverse proxy in
front of the HTTP port. Tassandra handles only pricing; Aperture handles
macaroon issuance and Lightning payment enforcement.
