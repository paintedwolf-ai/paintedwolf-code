// Package admissionstate holds the host process's confinement activation.
package admissionstate

import "sync/atomic"

var ProcessEnabled atomic.Bool
