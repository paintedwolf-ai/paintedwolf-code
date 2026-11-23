//go:build !darwin && !linux

package privateguesthost

import (
	"context"
	"net"

	"github.com/lycaon/lycaon/internal/serviceguest"
)

func launchService(context.Context) (net.Conn, error) {
	return nil, serviceguest.ErrUnavailable
}
