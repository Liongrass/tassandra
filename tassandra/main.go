package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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

	// Ensure the data directory exists before opening the log file.
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return fmt.Errorf("creating data directory %s: %w",
			cfg.DataDir, err)
	}

	// Open the log file in append mode, creating it if necessary.
	logPath := filepath.Join(cfg.DataDir, "tassandra.log")
	logFile, err := os.OpenFile(
		logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644,
	)
	if err != nil {
		return fmt.Errorf("opening log file %s: %w", logPath, err)
	}
	defer logFile.Close()

	// Set up structured logging via btclog, writing to stdout and the
	// log file simultaneously.
	logger := buildLogger(cfg.LogLevel, io.MultiWriter(os.Stdout, logFile))
	tassandra.UseLogger(logger)
	oracle.UseLogger(logger)
	pricefeed.UseLogger(logger)
	pricestore.UseLogger(logger)

	logger.Infof("Tassandra starting")
	logger.Infof("Data directory: %s", cfg.DataDir)
	logger.Infof("Log file: %s", logPath)

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

	// Build active price feeds with their per-exchange currency lists.
	feedCfgs := buildFeeds(cfg)
	if len(feedCfgs) == 0 {
		return fmt.Errorf("no exchange feeds enabled — " +
			"configure at least one exchange with a currency under [Exchange]")
	}

	logger.Infof("Active feeds: %d", len(feedCfgs))

	pollInterval, err := time.ParseDuration(cfg.PollInterval)
	if err != nil {
		return fmt.Errorf("invalid pollinterval %q: %w", cfg.PollInterval, err)
	}

	// Create and start the oracle.
	o, err := oracle.New(oracle.Config{
		Feeds:        feedCfgs,
		Store:        store,
		PollInterval: pollInterval,
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

	// Write PID file so tassandra-cli stop can find us.
	pidPath := filepath.Join(cfg.DataDir, "tassandra.pid")
	if err := writePIDFile(pidPath); err != nil {
		return fmt.Errorf("writing PID file: %w", err)
	}
	defer os.Remove(pidPath)

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

// writePIDFile writes the current process ID to path.
func writePIDFile(path string) error {
	pid := strconv.Itoa(os.Getpid())
	return os.WriteFile(path, []byte(pid), 0600)
}

// buildFeeds constructs the enabled price feed adapters from per-exchange
// currency lists. An exchange is included only when at least one currency is
// configured for it.
func buildFeeds(cfg *config) []oracle.FeedConfig {
	const timeout = 10 * time.Second

	// parseCurrencies splits a comma-separated currency string into a
	// deduplicated slice of FiatCurrency values. Returns nil if s is empty.
	parseCurrencies := func(s string) []pricefeed.FiatCurrency {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		parts := strings.Split(s, ",")
		seen := make(map[pricefeed.FiatCurrency]struct{}, len(parts))
		out := make([]pricefeed.FiatCurrency, 0, len(parts))
		for _, p := range parts {
			c := pricefeed.FiatCurrency(strings.ToUpper(strings.TrimSpace(p)))
			if c == "" {
				continue
			}
			if _, ok := seen[c]; !ok {
				seen[c] = struct{}{}
				out = append(out, c)
			}
		}
		return out
	}

	feeds := make([]oracle.FeedConfig, 0, 4)

	if currencies := parseCurrencies(cfg.Exchange.Binance); len(currencies) > 0 {
		feeds = append(feeds, oracle.FeedConfig{
			Feed:       pricefeed.NewBinanceFeed(timeout),
			Currencies: currencies,
		})
	}
	if currencies := parseCurrencies(cfg.Exchange.Kraken); len(currencies) > 0 {
		feeds = append(feeds, oracle.FeedConfig{
			Feed:       pricefeed.NewKrakenFeed(timeout),
			Currencies: currencies,
		})
	}
	if currencies := parseCurrencies(cfg.Exchange.Coinbase); len(currencies) > 0 {
		feeds = append(feeds, oracle.FeedConfig{
			Feed:       pricefeed.NewCoinbaseFeed(timeout),
			Currencies: currencies,
		})
	}
	if currencies := parseCurrencies(cfg.Exchange.Bitstamp); len(currencies) > 0 {
		feeds = append(feeds, oracle.FeedConfig{
			Feed:       pricefeed.NewBitstampFeed(timeout),
			Currencies: currencies,
		})
	}

	return feeds
}

// buildLogger constructs a btclog logger at the given level writing to w.
func buildLogger(levelStr string, w io.Writer) btclog.Logger {
	handler := btclog.NewDefaultHandler(w)

	if level, ok := btclog.LevelFromString(strings.ToUpper(levelStr)); ok {
		handler.SetLevel(level)
	} else {
		handler.SetLevel(btclog.LevelInfo)
	}

	return btclog.NewSLogger(handler)
}
