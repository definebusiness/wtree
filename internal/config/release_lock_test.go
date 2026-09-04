package config_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/definebusiness/wtree/internal/config"
)

func TestReleaseLockRoundTripAndCanonicalOrder(t *testing.T) {
	manifest := []byte("version: 2\nproject: example\n")
	digest := sha256.Sum256(manifest)
	lock := config.ReleaseLock{Version: config.ReleaseLockVersion, Project: config.ReleaseLockProject{ID: "project", ManifestSHA256: hex.EncodeToString(digest[:])}, Release: config.ReleaseLockRelease{Name: "v1.4.0"}, Repositories: map[string]config.ReleaseLockRepository{"zebra": {Revision: strings.Repeat("a", 40)}, "api": {Revision: strings.Repeat("b", 64)}}}
	encoded, err := config.MarshalReleaseLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "  api:\n") || strings.Index(string(encoded), "  api:") > strings.Index(string(encoded), "  zebra:") {
		t.Fatalf("repositories not lexical:\n%s", encoded)
	}
	decoded, err := config.LoadReleaseLock(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := decoded.ValidateFor("project", manifest, []string{"api", "zebra"}); err != nil {
		t.Fatal(err)
	}
	again, err := config.MarshalReleaseLock(decoded)
	if err != nil || string(encoded) != string(again) {
		t.Fatalf("canonical roundtrip changed: %v\n%s\n%s", err, encoded, again)
	}
}

func TestReleaseLockRejectsInvalidContract(t *testing.T) {
	good := "version: 1\nproject:\n  id: project\n  manifest_sha256: " + strings.Repeat("a", 64) + "\nrelease:\n  name: v1\nrepositories:\n  api:\n    revision: " + strings.Repeat("b", 40) + "\n"
	for name, input := range map[string]string{
		"unknown":            strings.Replace(good, "version: 1", "version: 1\nextra: no", 1),
		"bad revision":       strings.Replace(good, strings.Repeat("b", 40), "ABC", 1),
		"blank name":         strings.Replace(good, "name: v1", "name: ''", 1),
		"control name":       strings.Replace(good, "name: v1", "name: \"v\\n1\"", 1),
		"multiple documents": good + "---\nversion: 1\n",
	} {
		if _, err := config.LoadReleaseLock([]byte(input)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestReleaseLockBindsExactManifestAndRepositorySet(t *testing.T) {
	manifest := []byte("exact bytes\n")
	digest := sha256.Sum256(manifest)
	lock := config.ReleaseLock{Version: 1, Project: config.ReleaseLockProject{ID: "project", ManifestSHA256: hex.EncodeToString(digest[:])}, Release: config.ReleaseLockRelease{Name: "v"}, Repositories: map[string]config.ReleaseLockRepository{"api": {Revision: strings.Repeat("a", 40)}}}
	if err := lock.ValidateFor("project", manifest, []string{"api"}); err != nil {
		t.Fatal(err)
	}
	if err := lock.ValidateFor("other", manifest, []string{"api"}); err == nil {
		t.Fatal("wrong project accepted")
	}
	if err := lock.ValidateFor("project", []byte("exact bytes\r\n"), []string{"api"}); err == nil {
		t.Fatal("wrong bytes accepted")
	}
	if err := lock.ValidateFor("project", manifest, []string{"other"}); err == nil {
		t.Fatal("wrong set accepted")
	}
}
