//go:build linux

package linuxsandbox

import "sync"

// hostSockets records, by socket cookie, the sockets the supervisor put in
// the host's network namespace, true for granted listeners.
type hostSockets struct {
	mu      sync.Mutex
	cookies map[uint64]bool
}

func newHostSockets() *hostSockets { return &hostSockets{cookies: map[uint64]bool{}} }

func (h *hostSockets) lookup(cookie uint64) (host, listener bool) {
	if cookie == 0 {
		return false, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	listener, host = h.cookies[cookie]
	return host, listener
}

func (h *hostSockets) mark(cookie uint64, listener bool) {
	h.mu.Lock()
	h.cookies[cookie] = listener
	h.mu.Unlock()
}

func (h *hostSockets) forget(cookie uint64) {
	h.mu.Lock()
	delete(h.cookies, cookie)
	h.mu.Unlock()
}
