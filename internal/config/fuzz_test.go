package config_test

import (
	"testing"

	"github.com/marcel/wtree/internal/config"
)

func FuzzLoadVersionedConfig(f *testing.F) {
	for _, seed := range []string{"version: 1\n", "version: 1\nproject:\n  id: p\nrepositories:\n  root:\n    source: .\n    mount: .\n", "version: 2\n"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		_, _ = config.LoadProject([]byte(input))
		_, _ = config.LoadGlobal([]byte(input))
	})
}
