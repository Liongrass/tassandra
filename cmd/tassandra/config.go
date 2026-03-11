package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	flags "github.com/jessevdk/go-flags"

	tassandra "github.com/liongrass/tassandra"
	"github.com/liongrass/tassandra/pricefeed"
)

const (
	defaultGRPCListen    = "0.0.0.0:10590"
	defaultHTTPListen    = "0.0.0.0:10591"
	defaultLogLevel      = "info"
	defaultDBFileName    = "tassandra.db"
	defaultConfigFile    = "tassandra.conf"
	defaultDataDirName   = ".tassandra"
	defaultFetchTimeout  = "10s"
	defaultPollInterval  = "60s"
)

// config holds all daemon configuration. Fields are populated from the ini
// config file and command-line flags via go-flags.
type config struct {
	GRPCListen string `long:"grpclisten" description:"gRPC server listen address" default:"0.0.0.0:10590"`
	HTTPListen string `long:"httplisten" description:"HTTP server listen address" default:"0.0.0.0:10591"`
	LogLevel   string `long:"loglevel"   description:"Logging level (trace|debug|info|warn|error|critical)" default:"info"`
	DataDir    string `long:"datadir"    description:"Directory for database and config files"`
	ConfigFile string `long:"configfile" short:"C" description:"Path to config file"`

	Exchange exchangeConfig `group:"Exchange" namespace:"exchange"`
	Asset    assetSection   `group:"Asset"    namespace:"asset"`
}

// exchangeConfig lists the fiat currencies to poll for each exchange adapter.
// An exchange is enabled only when at least one currency is listed for it.
// Repeat the flag for each currency, e.g.: binance=USD binance=EUR
type exchangeConfig struct {
	Binance  []string `long:"binance"  description:"Enable Binance for this currency (repeat for multiple)"`
	Kraken   []string `long:"kraken"   description:"Enable Kraken for this currency (repeat for multiple)"`
	Coinbase []string `long:"coinbase" description:"Enable Coinbase for this currency (repeat for multiple)"`
	Bitstamp []string `long:"bitstamp" description:"Enable Bitstamp for this currency (repeat for multiple)"`
}

// assetSection holds zero or more asset configuration strings. Each entry
// has the format:  <hex-asset-id>:<currency>:<markup-ppm>
// For example:     0a1b2c...:USD:5000
type assetSection struct {
	Assets []string `long:"asset" description:"Asset config as hex-id:currency:markup-ppm (repeat for each asset)"`
}

// loadConfig parses the config file and command-line flags, applies defaults
// for any missing values, and returns the validated config.
func loadConfig() (*config, error) {
	// Defaults that depend on the home directory are set here rather than
	// in struct tags so they resolve correctly at runtime.
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving home directory: %w", err)
	}

	defaultDataDir := filepath.Join(homeDir, defaultDataDirName)

	cfg := &config{
		DataDir:    defaultDataDir,
		ConfigFile: filepath.Join(defaultDataDir, defaultConfigFile),
	}

	// First pass: parse only the datadir and configfile flags so we know
	// where to look for the ini file.
	preCfg := &config{}
	preParser := flags.NewParser(preCfg, flags.IgnoreUnknown)
	if _, err := preParser.Parse(); err != nil {
		return nil, err
	}

	if preCfg.DataDir != "" {
		cfg.DataDir = cleanPath(preCfg.DataDir)
	}
	if preCfg.ConfigFile != "" {
		cfg.ConfigFile = cleanPath(preCfg.ConfigFile)
	}

	// Parse the ini config file if it exists.
	parser := flags.NewParser(cfg, flags.Default)
	iniParser := flags.NewIniParser(parser)

	if err := iniParser.ParseFile(cfg.ConfigFile); err != nil {
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) {
			return nil, fmt.Errorf("parsing config file %s: %w",
				cfg.ConfigFile, err)
		}
		// Config file not found — proceed with defaults and flags only.
	}

	// Second pass: let command-line flags override the config file.
	if _, err := parser.Parse(); err != nil {
		var flagsErr *flags.Error
		if errors.As(err, &flagsErr) &&
			flagsErr.Type == flags.ErrHelp {
			os.Exit(0)
		}
		return nil, err
	}

	cfg.DataDir = cleanPath(cfg.DataDir)

	return cfg, nil
}

// assetConfigs parses the [Asset] section strings into AssetConfig values.
func (cfg *config) assetConfigs() ([]tassandra.AssetConfig, error) {
	result := make([]tassandra.AssetConfig, 0, len(cfg.Asset.Assets))
	for _, s := range cfg.Asset.Assets {
		ac, err := parseAssetString(s)
		if err != nil {
			return nil, fmt.Errorf("invalid asset config %q: %w", s, err)
		}
		result = append(result, ac)
	}
	return result, nil
}

// parseAssetString parses a string of the form
// "<hex-asset-id>:<currency>:<markup-ppm>" into an AssetConfig.
func parseAssetString(s string) (tassandra.AssetConfig, error) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 {
		return tassandra.AssetConfig{},
			fmt.Errorf("expected <hex-id>:<currency>:<markup-ppm>, got %q", s)
	}

	assetID := strings.ToLower(strings.TrimSpace(parts[0]))
	if assetID == "" {
		return tassandra.AssetConfig{}, errors.New("asset ID is empty")
	}

	currency := pricefeed.FiatCurrency(
		strings.ToUpper(strings.TrimSpace(parts[1])),
	)
	if currency == "" {
		return tassandra.AssetConfig{}, errors.New("currency is empty")
	}

	ppm, err := strconv.ParseUint(strings.TrimSpace(parts[2]), 10, 64)
	if err != nil {
		return tassandra.AssetConfig{},
			fmt.Errorf("markup ppm must be an integer: %w", err)
	}

	return tassandra.AssetConfig{
		AssetID:   assetID,
		Currency:  currency,
		MarkupPPM: ppm,
	}, nil
}

// cleanPath expands a leading ~ and cleans the path.
func cleanPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	return filepath.Clean(path)
}
