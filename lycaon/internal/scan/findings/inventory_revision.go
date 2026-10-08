package findings

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/lycaon/lycaon/pkg/api"
)

// InventoryRevision binds all query views to the complete scan inventory.
func InventoryRevision(scans []api.CodeScan) string {
	ids := make([]string, 0, len(scans))
	for _, scan := range scans {
		ids = append(ids, scan.ID)
	}
	slices.Sort(ids)
	var rows []string
	for _, finding := range InventoryFindings(scans) {
		raw, _ := json.Marshal(finding)
		rows = append(rows, string(raw))
	}
	slices.Sort(rows)
	raw, _ := json.Marshal(struct {
		IDs      []string
		Findings []string
	}{ids, rows})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
