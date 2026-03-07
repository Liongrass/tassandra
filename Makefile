GO      ?= go
GOBUILD  = $(GO) build
GOTEST   = $(GO) test
GOINSTALL = $(GO) install

PROTOC         ?= protoc
PROTOC_GEN_GO  ?= protoc-gen-go
PROTOC_GEN_GRPC ?= protoc-gen-go-grpc
SQLC           ?= sqlc

BIN_DIR  = bin
DAEMON   = $(BIN_DIR)/tassandra
CLI      = $(BIN_DIR)/tassandra-cli

DAEMON_PKG = ./cmd/tassandra
CLI_PKG    = ./cmd/tassandra-cli

.PHONY: all build daemon cli install test vet proto sqlc clean

all: build

# ── Build ─────────────────────────────────────────────────────────────────────

build: daemon cli

daemon:
	@mkdir -p $(BIN_DIR)
	$(GOBUILD) -o $(DAEMON) $(DAEMON_PKG)
	@echo "Built $(DAEMON)"

cli:
	@mkdir -p $(BIN_DIR)
	$(GOBUILD) -o $(CLI) $(CLI_PKG)
	@echo "Built $(CLI)"

# Install both binaries into $GOPATH/bin (or ~/go/bin by default).
install:
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

# ── Clean ─────────────────────────────────────────────────────────────────────

clean:
	rm -rf $(BIN_DIR)
	@echo "Cleaned build artifacts"
