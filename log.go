package tassandra

import "github.com/btcsuite/btclog/v2"

// log is the package-level logger for the tassandra package.
var log = btclog.Disabled

// UseLogger sets the package-level logger, enabling the caller to direct
// tassandra log output to the application's logging subsystem.
func UseLogger(logger btclog.Logger) {
	log = logger
}
