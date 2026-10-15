//go:build !confinehelpermain

package confine

func init() {
	// Helper argv is handled here, before main, so any binary that links this
	// package can be the helper.
	runHelperIfInvoked()
}
