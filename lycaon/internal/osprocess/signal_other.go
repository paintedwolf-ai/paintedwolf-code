//go:build !darwin && !linux

package osprocess

import (
	"errors"
	"syscall"
)

// Signal has no start-time incarnation check on this platform.
func Signal(Incarnation, syscall.Signal) error { return errors.ErrUnsupported }

// SignalGroup has no start-time incarnation check on this platform.
func SignalGroup(Incarnation, syscall.Signal) error { return errors.ErrUnsupported }
