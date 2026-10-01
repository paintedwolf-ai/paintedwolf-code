package evidence

import "strings"

type artifactVerifier struct{}

func (artifactVerifier) Verify(rec Record, claim Claim) (bool, string) {
	if claim.Path != "" && !pathObservedInRecord(rec, claim.Path) {
		return false, ""
	}
	if strings.TrimSpace(claim.Excerpt) == "" {
		return true, ""
	}
	if excerptInRecordBodies(rec, claim.Excerpt) {
		return true, ""
	}
	return false, ""
}
