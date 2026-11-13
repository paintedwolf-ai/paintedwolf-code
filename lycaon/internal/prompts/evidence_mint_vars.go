package prompts

import "github.com/lycaon/lycaon/internal/evidence"

// evidenceMints is the mints template fact: whether the reader's tools can mint
// each evidence kind, so copy names a handle only where one can be held.
func evidenceMints(reachHeld map[string]bool) map[string]bool {
	minted, err := evidence.ActiveBinding().MintedKinds(reachHeld)
	if err != nil {
		return map[string]bool{}
	}
	return minted
}
