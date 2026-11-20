//go:build !darwin && !linux

package privateguesthost

import (
	"context"

	"github.com/lycaon/lycaon/internal/serviceguest"
)

// OpenPrivateGuest reports an unavailable private service runtime.
func OpenPrivateGuest(context.Context) (*serviceguest.Client, error) {
	return nil, serviceguest.ErrUnavailable
}
