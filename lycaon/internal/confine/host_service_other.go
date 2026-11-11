//go:build !linux && !darwin

package confine

import "github.com/lycaon/lycaon/internal/socketref"

// ServeHostSocket is unavailable where no confinement realizes sockets.
func ServeHostSocket(path string) (HostService, func(), error) {
	return HostService{}, nil, &SocketResolveError{Code: SocketResolveRefused, Path: path, Err: socketref.ErrUnsupported}
}
