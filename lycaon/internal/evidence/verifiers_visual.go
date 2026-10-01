package evidence

import "strings"

// visualIntentVerifier rejects authored mockups as runtime evidence.
type visualIntentVerifier struct{}

func (visualIntentVerifier) Verify(Record, Claim) (bool, string) {
	return false, ""
}

// surfaceSnapshotVerifier checks excerpts against captured runtime state.
type surfaceSnapshotVerifier struct{}

func (surfaceSnapshotVerifier) Verify(rec Record, claim Claim) (bool, string) {
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

// pageGeometryVerifier checks excerpts against geometry reports.
type pageGeometryVerifier struct{}

func (pageGeometryVerifier) Verify(rec Record, claim Claim) (bool, string) {
	if strings.TrimSpace(claim.Excerpt) == "" {
		return true, ""
	}
	if excerptInRecordBodies(rec, claim.Excerpt) {
		return true, ""
	}
	return false, ""
}
