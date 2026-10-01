package evidence

import "strings"

type verbatimShapeVerifier struct{}

func (verbatimShapeVerifier) Verify(rec Record, claim Claim) (bool, string) {
	if strings.TrimSpace(claim.Excerpt) == "" {
		return true, ""
	}
	if excerptInRecordBodies(rec, claim.Excerpt) {
		return true, ""
	}
	return false, ""
}
