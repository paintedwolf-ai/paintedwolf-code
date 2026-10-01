package providerauth

import (
	"strings"
)

func ValidRegionIdentifier(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 64 || raw[0] == '-' || raw[len(raw)-1] == '-' {
		return false
	}
	for _, char := range raw {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}
