package config_test

import (
	"testing"

	"github.com/definebusiness/wtree/internal/config"
)

// TestVersionedConfigFuzzSeeds keeps the original local-config corpus visible
// to ordinary test runs; the same corpus is also exercised by Fuzz.
func TestVersionedConfigFuzzSeeds(t *testing.T) {
	for _, input := range []string{
		"version: 1\n",
		"version: 1\nproject:\n  id: p\nrepositories:\n  root:\n    source: .\n    mount: .\n",
		"version: 2\n",
	} {
		_, _ = config.LoadProject([]byte(input))
		_, _ = config.LoadGlobal([]byte(input))
	}
}
