package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/btcsuite/btclog/v2"
	"google.golang.org/grpc"

	tassandra "github.com/liongrass/tassandra"
	"github.com/liongrass/tassandra/oracle"
	"github.com/liongrass/tassandra/pricefeed"
	"github.com/liongrass/tassandra/pricestore"
	"github.com/liongrass/tassandra/tassandrarpc/priceoraclerpc"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Set up structured logging via btclog.
	logger := buildLogger(cfg.LogLevel)
	tassandra.UseLogger(logger)
	oracle.UseLogger(logger)
	pricefeed.UseLogger(logger)
	pricestore.UseLogger(logger)

	logger.Infof("Tassandra starting")
	logger.Infof("Data directory: %s", cfg.DataDir)

	// Ensure the data directory exists.
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return fmt.Errorf("creating data directory %s: %w",
			cfg.DataDir, err)
	}

	// Open the price store.
	dbPath := filepath.Join(cfg.DataDir, defaultDBFileName)
	store, err := pricestore.NewSQLiteStore(dbPath)
	if err != nil {
		return fmt.Errorf("opening price store: %w", err)
	}
	defer store.Close()

	// Parse asset configurations.
	assetCfgs, err := cfg.assetConfigs()
	if err != nil {
		return fmt.Errorf("parsing asset config: %w", err)
	}

	if len(assetCfgs) == 0 {
		logger.Warnf("No assets configured — gRPC QueryAssetRates will " +
			"return UNSUPPORTED for all requests")
	}

	// Resolve fiat currencies to poll (explicit list + asset currencies).
	currencies := cfg.fiatCurrencies(assetCfgs)
	logger.Infof("Tracking currencies: %v", currencies)

	// Build active price feeds.
	feeds := buildFeeds(cfg)
	if len(feeds) == 0 {
		return fmt.Errorf("no exchange feeds enabled — " +
			"enable at least one under [Exchange]")
	}

	logger.Infof("Active feeds: %d", len(feeds))

	// Create and start the oracle.
	o, err := oracle.New(oracle.Config{
		Feeds:        feeds,
		Store:        store,
		Currencies:   currencies,
		PollInterval: time.Minute,
		FetchTimeout: 10 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("creating oracle: %w", err)
	}

	if err := o.Start(); err != nil {
		return fmt.Errorf("starting oracle: %w", err)
	}
	defer o.Stop()

	// Set up the gRPC server.
	rpcSrv := tassandra.NewRpcServer(o, assetCfgs)

	grpcSrv := grpc.NewServer()
	priceoraclerpc.RegisterPriceOracleServer(grpcSrv, rpcSrv)

	grpcLis, err := net.Listen("tcp", cfg.GRPCListen)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", cfg.GRPCListen, err)
	}

	grpcErrCh := make(chan error, 1)
	go func() {
		logger.Infof("gRPC server listening on %s", cfg.GRPCListen)
		if err := grpcSrv.Serve(grpcLis); err != nil {
			grpcErrCh <- err
		}
	}()
	defer grpcSrv.GracefulStop()

	// Set up and start the HTTP server.
	httpSrv := tassandra.NewHTTPServer(cfg.HTTPListen, o, store)
	if err := httpSrv.Start(); err != nil {
		return fmt.Errorf("starting HTTP server: %w", err)
	}

	logger.Infof("Tassandra ready")

	// Wait for an interrupt/termination signal or a server error.
	ctx, stop := signal.NotifyContext(
		context.Background(), syscall.SIGINT, syscall.SIGTERM,
	)
	defer stop()

	select {
	case <-ctx.Done():
		logger.Infof("Shutdown signal received")

	case err := <-grpcErrCh:
		return fmt.Errorf("gRPC server error: %w", err)
	}

	// Graceful shutdown with a 30-second deadline.
	shutdownCtx, cancel := context.WithTimeout(
		context.Background(), 30*time.Second,
	)
	defer cancel()

	if err := httpSrv.Stop(shutdownCtx); err != nil {
		logger.Errorf("HTTP server shutdown: %v", err)
	}

	logger.Infof("Tassandra stopped")

	return nil
}

// buildFeeds constructs the enabled price feed adapters.
func buildFeeds(cfg *config) []pricefeed.PriceFeed {
	const timeout = 10 * time.Second

	feeds := make([]pricefeed.PriceFeed, 0, 4)

	if !cfg.Exchange.DisableBinance {
		feeds = append(feeds, pricefeed.NewBinanceFeed(timeout))
	}
	if !cfg.Exchange.DisableKraken {
		feeds = append(feeds, pricefeed.NewKrakenFeed(timeout))
	}
	if !cfg.Exchange.DisableCoinbase {
		feeds = append(feeds, pricefeed.NewCoinbaseFeed(timeout))
	}
	if !cfg.Exchange.DisableBitstamp {
		feeds = append(feeds, pricefeed.NewBitstampFeed(timeout))
	}

	return feeds
}

// buildLogger constructs a btclog logger at the given level writing to stdout.
func buildLogger(levelStr string) btclog.Logger {
	handler := btclog.NewDefaultHandler(os.Stdout)

	if level, ok := btclog.LevelFromString(strings.ToUpper(levelStr)); ok {
		handler.SetLevel(level)
	} else {
		handler.SetLevel(btclog.LevelInfo)
	}

	return btclog.NewSLogger(handler)
}
