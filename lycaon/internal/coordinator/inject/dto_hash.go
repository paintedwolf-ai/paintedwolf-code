package inject

import (
	"crypto/sha256"
	"encoding/hex"
)

func boolString(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
func hashString(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:8]) }
