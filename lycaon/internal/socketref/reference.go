// Package socketref owns a retained local-service destination, independently of
// the pathname used to request it. Authorization remains with the grant owner.
package socketref

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
)

// ErrUnbound requires fresh approval of a service whose reference is unavailable.
var ErrUnbound = errors.New("local service reference is unavailable; approve the current service again")

// ErrUnsupported names a platform with no object-bound local-service realization.
var ErrUnsupported = errors.New("this platform cannot retain a local-service recipient; use a separately approved service endpoint")

// Reference holds one destination until all operations have released it.
type Reference struct {
	mu         sync.RWMutex
	file       *os.File
	path       string
	address    string
	release    func() error
	verify     func(net.Conn) error
	invalid    atomic.Bool
	endpointMu sync.Mutex
	endpoints  map[string]serviceEndpoint
}

type serviceEndpoint interface {
	Path() string
	Close() error
}

// Close releases the destination after in-flight connects have finished.
func (r *Reference) Close() error {
	if r == nil {
		return nil
	}
	r.endpointMu.Lock()
	defer r.endpointMu.Unlock()
	var endpointErr error
	for _, endpoint := range r.endpoints {
		endpointErr = errors.Join(endpointErr, endpoint.Close())
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
	err := errors.Join(endpointErr, releaseErr, r.file.Close())
	r.file = nil
	return err
}

// DialContext connects while retaining the destination reference through the
// kernel's destination lookup. It never retries through the requested pathname.
func (r *Reference) DialContext(ctx context.Context) (net.Conn, error) {
	return r.dialContext(ctx, "unix", "")
}

func (r *Reference) dialContext(ctx context.Context, network, local string) (net.Conn, error) {
	if r == nil {
		return nil, ErrUnbound
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.file == nil || r.invalid.Load() {
		return nil, ErrUnbound
	}
	var dialer net.Dialer
	if local != "" {
		dialer.LocalAddr = &net.UnixAddr{Net: network, Name: local}
	}
	address := r.address
	if address == "" {
		address = descriptorPath(r.file)
	}
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	if r.verify != nil {
		if err := r.verify(conn); err != nil {
			r.invalid.Store(true)
			_ = conn.Close()
			return nil, errors.Join(ErrUnbound, err)
		}
	}
	if r.invalid.Load() {
		_ = conn.Close()
		return nil, ErrUnbound
	}
	return conn, nil
}

// Endpoint exposes the retained destination through a protected, bounded relay.
// Closing the last authority closes and joins the relay before releasing it.
func (r *Reference) Endpoint(network string) (string, error) {
	r.endpointMu.Lock()
	defer r.endpointMu.Unlock()
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.file == nil || r.invalid.Load() || network != "unix" && network != "unixgram" {
		return "", ErrUnbound
	}
	if r.endpoints == nil {
		r.endpoints = make(map[string]serviceEndpoint, 2)
	}
	if r.endpoints[network] == nil {
		var err error
		r.endpoints[network], err = newReferenceEndpoint(r, network)
		if err != nil {
			return "", err
		}
	}
	return r.endpoints[network].Path(), nil
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
