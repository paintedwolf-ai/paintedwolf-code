package findings

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/lycaon/lycaon/pkg/api"
)

// PrimaryFingerprint computes fingerprints.primary per docs/scan-findings.md#fingerprint.
func PrimaryFingerprint(driverID string, kind api.FindingKind, ruleID, uri string, startLine int, osvID string) string {
	payload := fmt.Sprintf("%s|%s|%s|%s|%d|%s", driverID, kind, ruleID, uri, startLine, osvID)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])[:16]
}

// PrimaryURI returns the first location uri, or empty when none.
func PrimaryURI(f api.SecurityFinding) string {
	if len(f.Locations) == 0 {
		return ""
	}
	return f.Locations[0].URI
}

// PrimaryLine returns the first location start_line, or 0 when unset.
func PrimaryLine(f api.SecurityFinding) int {
	if len(f.Locations) == 0 {
		return 0
	}
	return f.Locations[0].StartLine
}

// RefreshFingerprint recomputes fingerprints.primary from the finding body.
func RefreshFingerprint(f *api.SecurityFinding) {
	if f == nil {
		return
	}
	kind := api.FindingKindCustom
	osvID := ""
	if f.Properties != nil && f.Properties.Lycaon != nil {
		if f.Properties.Lycaon.Kind != "" {
			kind = f.Properties.Lycaon.Kind
		}
		if f.Properties.Lycaon.Advisory != nil {
			osvID = f.Properties.Lycaon.Advisory.OSVID
		}
	}
	driverID := f.Tool.DriverID
	if driverID == "" {
		driverID = "unknown"
	}
	f.Fingerprints.Primary = PrimaryFingerprint(driverID, kind, f.RuleID, PrimaryURI(*f), PrimaryLine(*f), osvID)
}

// StampScannerDriver sets tool.driver_id to the catalog scanner id and refreshes fingerprints.
func StampScannerDriver(findings []api.SecurityFinding, scannerID string) []api.SecurityFinding {
	if len(findings) == 0 || scannerID == "" {
		return findings
	}
	out := append([]api.SecurityFinding(nil), findings...)
	for i := range out {
		out[i].Tool.DriverID = scannerID
		RefreshFingerprint(&out[i])
	}
	return out
}
