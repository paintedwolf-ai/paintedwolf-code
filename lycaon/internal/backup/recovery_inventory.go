package backup

import (
	"encoding/json"
	"fmt"
	"math"
)

// RecoveryInventory binds a locally created snapshot to its complete manifest.
type RecoveryInventory struct {
	PayloadBytes   uint64 `json:"payload_bytes"`
	ManifestBytes  int    `json:"manifest_bytes"`
	ManifestSHA256 string `json:"manifest_sha256"`
	Entries        int    `json:"entries"`
}

func manifestInventory(manifest Manifest) (RecoveryInventory, error) {
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return RecoveryInventory{}, err
	}
	inventory := RecoveryInventory{ManifestBytes: len(raw), ManifestSHA256: sha256Hex(raw), Entries: len(manifest.Files)}
	for _, file := range manifest.Files {
		if file.Size < 0 || uint64(file.Size) >= math.MaxInt64-inventory.PayloadBytes {
			return RecoveryInventory{}, fmt.Errorf("recovery payload size overflows")
		}
		inventory.PayloadBytes += uint64(file.Size)
	}
	return inventory, nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
