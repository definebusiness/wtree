package store_test

import (
	"testing"

	"github.com/definebusiness/wtree/internal/store"
)

func TestDecodeRegistryUsesOneStrictCapturedGeneration(t *testing.T) {
	registry, err := store.DecodeRegistry([]byte(`{"version":1,"projects":{}}`))
	if err != nil || registry.Version != 1 || registry.Projects == nil {
		t.Fatalf("DecodeRegistry(valid) = %#v, %v", registry, err)
	}
	for _, data := range [][]byte{
		[]byte(`{"version":2,"projects":{}}`),
		[]byte(`{"version":1,"projects":{},"unknown":true}`),
		[]byte("{\"version\":1,\"projects\":{}}\n{}"),
	} {
		if _, err := store.DecodeRegistry(data); err == nil {
			t.Fatalf("DecodeRegistry(%s) error = nil", data)
		}
	}
}

func TestDecodeWorkspaceUsesOneStrictCapturedGeneration(t *testing.T) {
	state, err := store.DecodeWorkspace([]byte(`{"version":1,"id":"default","name":"default","path":"/project","repositories":{}}`))
	if err != nil || state.ID != "default" {
		t.Fatalf("DecodeWorkspace(valid) = %#v, %v", state, err)
	}
	if _, err := store.DecodeWorkspace([]byte(`{"version":2,"id":"default","name":"default","path":"/project","repositories":{}}`)); err == nil {
		t.Fatal("DecodeWorkspace(newer) error = nil")
	}
}
