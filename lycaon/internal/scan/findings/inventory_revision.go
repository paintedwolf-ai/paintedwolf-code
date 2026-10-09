package findings

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/lycaon/lycaon/pkg/api"
)

// InventoryRevision binds all query views to the complete scan inventory.
func InventoryRevision(scans []api.CodeScan) (string, error) {
	ids := make([]string, 0, len(scans))
	for _, scan := range scans {
		ids = append(ids, scan.ID)
	}
	slices.Sort(ids)
	var rows []string
	for _, finding := range InventoryFindings(scans) {
		raw, err := json.Marshal(finding)
		if err != nil {
			return "", fmt.Errorf("inventory revision: encode finding %s: %w", finding.RuleID, err)
		}
		rows = append(rows, string(raw))
	}
	slices.Sort(rows)
	raw, err := json.Marshal(struct {
		IDs      []string
		Findings []string
	}{ids, rows})
	if err != nil {
		return "", fmt.Errorf("inventory revision: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
