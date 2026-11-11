//go:build linux

package confine

import (
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/socketref"
)

// ServeHostSocket admits path, a socket the host listens on, to one action.
// The connect mediator dials the retained socket, so the client's identity
// must reach the service in band; release ends the retention.
func ServeHostSocket(path string) (HostService, func(), error) {
	lease, err := socketref.Capture(path)
	if err != nil {
		return HostService{}, nil, &SocketResolveError{Code: SocketResolveRefused, Path: path, Err: err}
	}
	grant := SocketGrant{ApprovedPath: path, ResolvedPath: fspath.CanonicalPath(path), Binding: lease.Binding()}
	return HostService{grant: &grant}, lease.Close, nil
}
