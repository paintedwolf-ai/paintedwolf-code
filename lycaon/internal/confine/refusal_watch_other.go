//go:build !darwin || !cgo

package confine

// newRefusalSource reports no source where no kernel adapter reports refusals.
func newRefusalSource(string) refusalSource { return nil }
