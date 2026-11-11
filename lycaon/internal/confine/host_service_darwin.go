//go:build darwin

package confine

import (
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/socketref"
)

// ServeHostSocket admits path, a socket the host listens on inside a
// socketref.ServiceDirectory, to one action. Seatbelt admits it by its exact
// path, which no confinement can repoint because every write floor denies
// that directory.
func ServeHostSocket(path string) (HostService, func(), error) {
	canonical := fspath.CanonicalPath(path)
	if canonical == "" || !socketref.IsServicePath(canonical) {
		return HostService{}, nil, &SocketResolveError{Code: SocketResolveRefused, Path: path, Err: fmt.Errorf("not inside a host service directory")}
	}
	info, err := os.Lstat(canonical)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return HostService{}, nil, &SocketResolveError{Code: SocketResolveNotSocket, Path: path, Err: err}
	}
	return HostService{path: canonical}, func() {}, nil
}
