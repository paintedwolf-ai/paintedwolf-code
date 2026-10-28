// Package socketref owns a retained local-service destination, independently of
// the pathname used to request it. Authorization remains with the grant owner.
package socketref

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
)

// ErrUnbound requires fresh approval of a service whose reference is unavailable.
var ErrUnbound = errors.New("local service reference is unavailable; approve the current service again")

// ErrUnsupported names a platform with no object-bound local-service realization.
var ErrUnsupported = errors.New("this platform cannot retain a local-service recipient; use a separately approved service endpoint")

// Reference holds one destination until all operations have released it.
type Reference struct {
	mu      sync.RWMutex
	file    *os.File
	path    string
	release func() error
}

// Close releases the destination after in-flight connects have finished.
func (r *Reference) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	var releaseErr error
	if r.release != nil {
		releaseErr = r.release()
	}
	err := errors.Join(releaseErr, r.file.Close())
	r.file = nil
	return err
}

// DialContext connects while retaining the destination reference through the
// kernel's destination lookup. It never retries through the requested pathname.
func (r *Reference) DialContext(ctx context.Context) (net.Conn, error) {
	if r == nil {
		return nil, ErrUnbound
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.file == nil {
		return nil, ErrUnbound
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "unix", descriptorPath(r.file))
}

// Duplicate transfers an independently owned reference to an execution boundary.
func (r *Reference) Duplicate() (*os.File, error) {
	if r == nil {
		return nil, ErrUnbound
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.file == nil {
		return nil, ErrUnbound
	}
	return duplicate(r.file)
}
