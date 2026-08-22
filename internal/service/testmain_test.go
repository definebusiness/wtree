package service

import (
	"flag"
	"os"
	"testing"
	"time"
)

const serviceTestTimeout = 15 * time.Minute

// TestMain gives the service integration package an explicit finite budget.
// The complete package intentionally exercises many independent real Git
// transactions and can exceed Go's implicit ten-minute default under race or
// cross-package contention. Explicit shorter caller-selected timeouts remain
// unchanged so focused timeout and cancellation tests keep their meaning.
func TestMain(m *testing.M) {
	flag.Parse()
	timeout := flag.Lookup("test.timeout")
	if timeout != nil {
		current, err := time.ParseDuration(timeout.Value.String())
		if err == nil && current == 10*time.Minute {
			_ = timeout.Value.Set(serviceTestTimeout.String())
		}
	}
	os.Exit(m.Run())
}
