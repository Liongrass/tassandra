package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/liongrass/tassandra/tassandrarpc/priceoraclerpc"
)

var (
	rpcServer  string
	httpServer string
	dataDir    string
)

func main() {
	root := &cobra.Command{
		Use:   "tassandra-cli",
		Short: "CLI client for the Tassandra Bitcoin price oracle daemon",
		// Silence cobra's built-in usage print on errors; we print our own.
		SilenceUsage: true,
	}

	home, _ := os.UserHomeDir()

	root.PersistentFlags().StringVar(&rpcServer, "rpcserver",
		"localhost:10590",
		"Tassandra gRPC server address")

	root.PersistentFlags().StringVar(&httpServer, "httpserver",
		"localhost:10591",
		"Tassandra HTTP server address")

	root.PersistentFlags().StringVar(&dataDir, "datadir",
		filepath.Join(home, ".tassandra"),
		"Tassandra data directory (used to locate the PID file)")

	root.AddCommand(
		newGetPriceCmd(),
		newGetRateCmd(),
		newStopCmd(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// ── getprice ──────────────────────────────────────────────────────────────────

func newGetPriceCmd() *cobra.Command {
	var (
		currency string
		date     int64
		exchange string
	)

	cmd := &cobra.Command{
		Use:   "getprice",
		Short: "Fetch the current or historical BTC price (no markup) via HTTP",
		Example: `  tassandra-cli getprice --currency USD
  tassandra-cli getprice --currency EUR --date 1709856000
  tassandra-cli getprice --currency GBP --exchange kraken --date 1709856000`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if exchange != "" && date == 0 {
				return fmt.Errorf(
					"--exchange requires --date",
				)
			}

			u := &url.URL{
				Scheme: "http",
				Host:   httpServer,
				Path:   "/price/" + currency,
			}

			if date != 0 {
				q := u.Query()
				q.Set("date", strconv.FormatInt(date, 10))
				if exchange != "" {
					q.Set("exchange", exchange)
				}
				u.RawQuery = q.Encode()
			}

			resp, err := http.Get(u.String())
			if err != nil {
				return fmt.Errorf(
					"connecting to HTTP server at %s: %w",
					httpServer, err,
				)
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return fmt.Errorf("reading response: %w", err)
			}

			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf(
					"server returned %d: %s",
					resp.StatusCode, string(body),
				)
			}

			var pr struct {
				Currency  string `json:"currency"`
				Price     string `json:"price"`
				Timestamp int64  `json:"timestamp"`
				Exchange  string `json:"exchange"`
			}
			if err := json.Unmarshal(body, &pr); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}

			fmt.Printf("Currency:  %s\n", pr.Currency)
			fmt.Printf("Price:     %s\n", pr.Price)
			fmt.Printf("Timestamp: %s\n",
				time.Unix(pr.Timestamp, 0).UTC().Format(time.RFC3339))
			if pr.Exchange != "" {
				fmt.Printf("Exchange:  %s\n", pr.Exchange)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&currency, "currency", "USD",
		"Fiat currency (USD, EUR, GBP)")
	cmd.Flags().Int64Var(&date, "date", 0,
		"Unix timestamp for historical query (0 = current price)")
	cmd.Flags().StringVar(&exchange, "exchange", "",
		"Exchange name for per-exchange historical query (requires --date)")

	return cmd
}

// ── getrate ───────────────────────────────────────────────────────────────────

func newGetRateCmd() *cobra.Command {
	var (
		assetID        string
		paymentAssetID string
		txType         string
	)

	cmd := &cobra.Command{
		Use:   "getrate",
		Short: "Query the gRPC PriceOracle for an asset rate (with markup)",
		Long: `Calls QueryAssetRates on the Tassandra gRPC server, which implements
the tapd-compatible PriceOracle interface. Returns the per-asset marked-up
exchange rate as configured in tassandra.conf.`,
		Example: `  tassandra-cli getrate --asset 0102030405...
  tassandra-cli getrate --asset 0102030405... --payment-asset deadbeef...
  tassandra-cli getrate --asset 0102030405... --type sale`,
		RunE: func(cmd *cobra.Command, args []string) error {
			txTypeEnum, err := parseTxType(txType)
			if err != nil {
				return err
			}

			conn, err := grpc.NewClient(
				rpcServer,
				grpc.WithTransportCredentials(
					insecure.NewCredentials(),
				),
			)
			if err != nil {
				return fmt.Errorf(
					"connecting to gRPC server at %s: %w",
					rpcServer, err,
				)
			}
			defer conn.Close()

			client := priceoraclerpc.NewPriceOracleClient(conn)

			req := &priceoraclerpc.QueryAssetRatesRequest{
				TransactionType: txTypeEnum,
				SubjectAsset: &priceoraclerpc.AssetSpecifier{
					Id: &priceoraclerpc.AssetSpecifier_AssetIdStr{
						AssetIdStr: assetID,
					},
				},
			}

			if paymentAssetID != "" {
				req.PaymentAsset = &priceoraclerpc.AssetSpecifier{
					Id: &priceoraclerpc.AssetSpecifier_AssetIdStr{
						AssetIdStr: paymentAssetID,
					},
				}
			}

			ctx, cancel := context.WithTimeout(
				context.Background(), 10*time.Second,
			)
			defer cancel()

			resp, err := client.QueryAssetRates(ctx, req)
			if err != nil {
				return fmt.Errorf("querying asset rates: %w", err)
			}

			switch r := resp.Result.(type) {
			case *priceoraclerpc.QueryAssetRatesResponse_Ok:
				printRates(assetID, paymentAssetID, txType, r.Ok.AssetRates)

			case *priceoraclerpc.QueryAssetRatesResponse_Error:
				return fmt.Errorf("oracle error [%v]: %s",
					r.Error.Code, r.Error.Message)

			default:
				return fmt.Errorf("unexpected response type")
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&assetID, "asset", "",
		"Hex asset ID of the subject asset to price")
	cmd.Flags().StringVar(&paymentAssetID, "payment-asset", "",
		"Hex asset ID of the payment asset (default: BTC / all-zeros)")
	cmd.Flags().StringVar(&txType, "type", "purchase",
		"Transaction type: purchase | sale")

	_ = cmd.MarkFlagRequired("asset")

	return cmd
}

// ── stop ──────────────────────────────────────────────────────────────────────

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Gracefully stop the running Tassandra daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			pidPath := filepath.Join(dataDir, "tassandra.pid")

			data, err := os.ReadFile(pidPath)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf(
						"PID file not found at %s — is tassandra running?",
						pidPath,
					)
				}
				return fmt.Errorf("reading PID file: %w", err)
			}

			pid, err := strconv.Atoi(string(data))
			if err != nil {
				return fmt.Errorf("invalid PID file contents: %w", err)
			}

			proc, err := os.FindProcess(pid)
			if err != nil {
				return fmt.Errorf("finding process %d: %w", pid, err)
			}

			if err := proc.Signal(syscall.SIGTERM); err != nil {
				return fmt.Errorf("sending SIGTERM to process %d: %w",
					pid, err)
			}

			fmt.Printf("Sent SIGTERM to tassandra (pid %d)\n", pid)

			return nil
		},
	}
}

// printRates formats and prints a QueryAssetRatesOkResponse.
func printRates(subjectAssetID, paymentAssetID, txType string,
	rates *priceoraclerpc.AssetRates) {

	fmt.Printf("Transaction type:     %s\n", txType)
	fmt.Printf("Subject asset:        %s\n", subjectAssetID)

	if paymentAssetID != "" {
		fmt.Printf("Payment asset:        %s\n", paymentAssetID)
	} else {
		fmt.Printf("Payment asset:        BTC (msats)\n")
	}

	fmt.Printf("\nSubject asset rate:\n")
	fmt.Printf("  Coefficient:        %s\n",
		rates.SubjectAssetRate.Coefficient)
	fmt.Printf("  Scale:              %d\n",
		rates.SubjectAssetRate.Scale)
	fmt.Printf("  Decimal value:      %s\n",
		fixedPointToDecimal(rates.SubjectAssetRate))

	fmt.Printf("\nPayment asset rate:\n")
	fmt.Printf("  Coefficient:        %s\n",
		rates.PaymentAssetRate.Coefficient)
	fmt.Printf("  Scale:              %d\n",
		rates.PaymentAssetRate.Scale)
	fmt.Printf("  Decimal value:      %s\n",
		fixedPointToDecimal(rates.PaymentAssetRate))

	expiry := time.Unix(int64(rates.ExpiryTimestamp), 0).UTC()
	fmt.Printf("\nExpires:              %s\n", expiry.Format(time.RFC3339))
}

// fixedPointToDecimal converts a FixedPoint message to a human-readable
// decimal string using big.Int arithmetic to avoid floating-point imprecision.
func fixedPointToDecimal(fp *priceoraclerpc.FixedPoint) string {
	if fp == nil {
		return "<nil>"
	}

	coeff, ok := new(big.Int).SetString(fp.Coefficient, 10)
	if !ok {
		return fmt.Sprintf("<invalid coefficient: %q>", fp.Coefficient)
	}

	if fp.Scale == 0 {
		return coeff.String()
	}

	divisor := new(big.Int).Exp(
		big.NewInt(10), big.NewInt(int64(fp.Scale)), nil,
	)

	intPart := new(big.Int).Div(coeff, divisor)
	fracPart := new(big.Int).Mod(coeff, divisor)

	return fmt.Sprintf("%s.%0*s", intPart, fp.Scale, fracPart)
}

// parseTxType maps a string to a TransactionType enum value.
func parseTxType(s string) (priceoraclerpc.TransactionType, error) {
	switch s {
	case "purchase", "":
		return priceoraclerpc.TransactionType_PURCHASE, nil
	case "sale":
		return priceoraclerpc.TransactionType_SALE, nil
	default:
		return 0, fmt.Errorf(
			"unknown transaction type %q: use 'purchase' or 'sale'", s,
		)
	}
}
