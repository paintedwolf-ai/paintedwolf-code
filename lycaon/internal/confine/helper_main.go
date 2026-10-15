//go:build confinehelpermain

package confine

// RunHelperIfInvoked runs the helper from main in a build that carries the
// confinehelpermain tag, where package initialization does not. A coverage
// build records only what runs after initialization.
func RunHelperIfInvoked() { runHelperIfInvoked() }
