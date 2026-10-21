//go:build !darwin && !linux

package hostenv

import "context"

// Windows identity and system facts need a native adapter; until one is
// qualified the utilities refuse rather than report a substitute.
func (OSHost) System(context.Context) (SystemFacts, error) {
	return SystemFacts{}, ErrHostFactUnavailable
}

func (OSHost) Identity(context.Context) (IdentityFacts, error) {
	return IdentityFacts{}, ErrHostFactUnavailable
}
