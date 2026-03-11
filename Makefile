GO      ?= go
GOTEST   = $(GO) test
GOINSTALL = $(GO) install

PROTOC          ?= protoc
PROTOC_GEN_GO   ?= protoc-gen-go
PROTOC_GEN_GRPC ?= protoc-gen-go-grpc
SQLC            ?= sqlc

DAEMON_PKG = ./tassandra
CLI_PKG    = ./tassandra-cli

.PHONY: all build install test vet proto sqlc

all: build

# ── Build / Install (to $GOPATH/bin) ─────────────────────────────────────────

build install:
	$(GOINSTALL) $(DAEMON_PKG)
	$(GOINSTALL) $(CLI_PKG)
	@echo "Installed tassandra and tassandra-cli"

# ── Test & vet ────────────────────────────────────────────────────────────────

test:
	$(GOTEST) ./...

vet:
	$(GO) vet ./...

# ── Code generation ───────────────────────────────────────────────────────────

# Regenerate gRPC/protobuf stubs from proto/priceoraclerpc/price_oracle.proto.
proto:
	$(PROTOC) \
		--proto_path=proto \
		--go_out=tassandrarpc --go_opt=paths=source_relative \
		--go-grpc_out=tassandrarpc --go-grpc_opt=paths=source_relative \
		proto/priceoraclerpc/price_oracle.proto
	@echo "Regenerated protobuf stubs"

# Regenerate sqlc query code from pricestore/sql/*.sql.
sqlc:
	cd pricestore && $(SQLC) generate
	@echo "Regenerated sqlc code"
