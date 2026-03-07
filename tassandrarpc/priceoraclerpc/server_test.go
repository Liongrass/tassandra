package priceoraclerpc_test

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/liongrass/tassandra/tassandrarpc/priceoraclerpc"
)

// TestQueryAssetRatesUnimplemented spins up a gRPC server backed by the
// generated UnimplementedPriceOracleServer and verifies that a client can
// connect and receive an Unimplemented response. This confirms that the
// generated stubs, service registration, and gRPC transport all work
// correctly before any real business logic is wired in.
func TestQueryAssetRatesUnimplemented(t *testing.T) {
	// Start a local TCP listener on a random port.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	// Register the unimplemented server and serve in the background.
	srv := grpc.NewServer()
	priceoraclerpc.RegisterPriceOracleServer(
		srv, &priceoraclerpc.UnimplementedPriceOracleServer{},
	)
	go srv.Serve(lis) //nolint:errcheck
	defer srv.GracefulStop()

	// Dial the server.
	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer conn.Close()

	client := priceoraclerpc.NewPriceOracleClient(conn)

	// Send a minimal QueryAssetRates request.
	resp, err := client.QueryAssetRates(
		context.Background(),
		&priceoraclerpc.QueryAssetRatesRequest{
			TransactionType: priceoraclerpc.TransactionType_PURCHASE,
			SubjectAsset: &priceoraclerpc.AssetSpecifier{
				Id: &priceoraclerpc.AssetSpecifier_AssetIdStr{
					AssetIdStr: "deadbeef",
				},
			},
		},
	)

	// The unimplemented server must return codes.Unimplemented.
	if err == nil {
		t.Fatalf("expected error, got response: %v", resp)
	}
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %v: %v",
			status.Code(err), err)
	}

	t.Logf("gRPC stack OK — server returned %v as expected",
		status.Code(err))
}
