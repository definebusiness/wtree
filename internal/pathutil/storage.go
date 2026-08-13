package pathutil

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode"
)

const storageDigestLength = 16

// StorageName returns a human-readable, stable directory name whose digest
// preserves uniqueness even when different logical names normalize alike.
func StorageName(logicalName string) string {
	digest := sha256.Sum256([]byte(logicalName))
	return fmt.Sprintf("%s-%x", storageSlug(logicalName), digest[:storageDigestLength])
}

// StorageNames derives a persisted logical-name mapping and rejects duplicate
// logical names or the extraordinarily unlikely derived-name collision.
func StorageNames(logicalNames []string) (map[string]string, error) {
	mapping := make(map[string]string, len(logicalNames))
	owners := make(map[string]string, len(logicalNames))
	for _, logicalName := range logicalNames {
		if _, exists := mapping[logicalName]; exists {
			return nil, fmt.Errorf("duplicate logical workspace name %q", logicalName)
		}
		storageName := StorageName(logicalName)
		if owner, exists := owners[storageName]; exists {
			return nil, fmt.Errorf("storage name %q collides for %q and %q", storageName, owner, logicalName)
		}
		mapping[logicalName] = storageName
		owners[storageName] = logicalName
	}
	return mapping, nil
}

func storageSlug(value string) string {
	var builder strings.Builder
	previousDash := false
	for _, character := range strings.ToLower(value) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			builder.WriteRune(character)
			previousDash = false
			continue
		}
		if !previousDash && builder.Len() > 0 {
			builder.WriteByte('-')
			previousDash = true
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if slug == "" {
		return "workspace"
	}
	return slug
}
