//go:build !darwin

package hostpower

import "errors"

type platformInhibitor struct{}

func (platformInhibitor) Supported() bool { return false }

func (platformInhibitor) Acquire() (lease, error) {
	return nil, errors.New("idle-sleep assertions are unavailable")
}
