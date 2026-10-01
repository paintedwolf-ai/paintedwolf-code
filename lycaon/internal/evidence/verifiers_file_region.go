package evidence

import (
	"strings"
)

type fileRegionVerifier struct{}

func (fileRegionVerifier) Verify(rec Record, claim Claim) (bool, string) {
	return VerifyFileRegion(rec, claim)
}

// VerifyFileRegion checks a path/line/excerpt claim against a read-shaped record.
func VerifyFileRegion(rec Record, claim Claim) (bool, string) {
	excerpt := strings.TrimSpace(claim.Excerpt)
	claimPath := effectiveClaimPath(rec, claim.Path)

	if claim.Line <= 0 && excerpt == "" {
		if claimPath != "" {
			if pathObservedInRecord(rec, claimPath) {
				return true, ""
			}
			return false, ""
		}
		return true, ""
	}

	if claim.Line <= 0 && excerpt != "" {
		if excerptInRecordBodies(rec, excerpt) {
			return true, ""
		}
		return false, ""
	}

	if excerpt != "" {
		if excerptAtCitedLine(rec, claimPath, claim.Line, excerpt, EvidenceLineBindWindow) {
			return true, ""
		}
		if rec.Survey && excerptInRecordBodies(rec, excerpt) {
			return true, ""
		}
		return false, ""
	}

	if claimPath != "" {
		if pathObservedInRecord(rec, claimPath) {
			return true, ""
		}
		return false, ""
	}
	return true, ""
}

func pathObservedInRecord(rec Record, path string) bool {
	path = NormalizeLedgerPath(path)
	if path == "" {
		return false
	}
	if rec.Path == path {
		return true
	}
	for _, p := range rec.pathsTouched {
		if p == path {
			return true
		}
	}
	if rec.grepLines != nil {
		if _, ok := rec.grepLines[path]; ok {
			return true
		}
	}
	return false
}
