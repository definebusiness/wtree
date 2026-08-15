package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/definebusiness/wtree/internal/store"
)

func FuzzReadVersionedJSON(f *testing.F) {
	for _, seed := range []string{
		`{"version":1,"id":"workspace","name":"workspace","path":"/tmp","repositories":{}}`,
		`{"version":2}`, `{"version":1,"unknown":true}`, `{`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		path := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = store.ReadWorkspace(path)
		_, _ = store.ReadRegistry(path)
		_, _ = store.ReadRecovery(path)
	})
}
