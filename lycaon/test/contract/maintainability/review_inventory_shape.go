package maintainability

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
)

// inventoryShape records dependency and directory membership identities.
func inventoryShape(inv *inventory, category, id string) (string, error) {
	if inv == nil {
		return "", fmt.Errorf("missing inventory for %s %s", category, id)
	}
	var paths []string
	var found bool
	switch category {
	case "ts_local_dependencies":
		paths, found = inv.dependencies[id]
	case "source_directories", "test_directories":
		paths, found = inv.directoryMembers[category][id]
	default:
		return "", fmt.Errorf("category %s has no inventory shape", category)
	}
	if !found || paths == nil {
		return "", fmt.Errorf("missing identities for %s %s", category, id)
	}
	canonical := append([]string{}, paths...)
	slices.Sort(canonical)
	raw, err := json.Marshal(slices.Compact(canonical))
	if err != nil {
		return "", fmt.Errorf("encode identities for %s %s: %w", category, id, err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}
