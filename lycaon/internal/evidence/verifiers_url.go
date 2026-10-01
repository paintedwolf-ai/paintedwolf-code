package evidence

import "strings"

type urlVerifier struct{}

func (urlVerifier) Verify(rec Record, claim Claim) (bool, string) {
	url := strings.TrimSpace(claim.URL)
	if url == "" {
		return false, ""
	}
	for _, observed := range ObservedURLsForRecord(rec) {
		if observed == url {
			return true, ""
		}
	}
	return false, ""
}
