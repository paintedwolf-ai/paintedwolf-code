//go:build !darwin && !linux

package hostpower

import "errors"

type platformInhibitor struct{}

func (platformInhibitor) Availability() error { return &UnavailableError{} }

func (platformInhibitor) Acquire() (lease, error) {
	return nil, errors.New("idle-sleep assertions are unavailable")
}
