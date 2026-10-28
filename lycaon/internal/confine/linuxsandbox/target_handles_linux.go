//go:build linux

package linuxsandbox

import (
	"errors"
	"io"
	"os"
	"strconv"
	"sync"

	"golang.org/x/sys/unix"
)

// maxCachedTargets bounds the processes whose handles the supervisor keeps.
const maxCachedTargets = 256

// processHandles are one sandbox process's pidfd and memory, kept across its
// notified calls: opening both again for every create cost more than the
// create. A pidfd names one process for its whole life, so a live one cannot
// stand for another that reused its number. A memory handle reads the address
// space it was opened on; after an exec that space has no users and reads
// return nothing, which reopens it.
type processHandles struct {
	pidfd int
	mu    sync.Mutex
	mem   *os.File
	// users counts calls holding the handles; an evicted entry closes when
	// the last one ends.
	users   int
	evicted bool
}

// targetHandles caches processHandles by thread group id.
type targetHandles struct {
	mu      sync.Mutex
	byGroup map[int]*processHandles
}

func newTargetHandles() *targetHandles {
	return &targetHandles{byGroup: map[int]*processHandles{}}
}

// acquire returns live handles for tgid, opening them when none are cached or
// the cached process has exited.
func (c *targetHandles) acquire(tgid int) (*processHandles, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h, ok := c.byGroup[tgid]; ok {
		if unix.PidfdSendSignal(h.pidfd, 0, nil, 0) == nil {
			h.users++
			return h, nil
		}
		c.evictLocked(tgid, h)
	}
	if len(c.byGroup) >= maxCachedTargets {
		c.sweepLocked()
	}
	pidfd, err := unix.PidfdOpen(tgid, 0)
	if err != nil {
		return nil, err
	}
	mem, err := os.Open("/proc/" + strconv.Itoa(tgid) + "/mem")
	if err != nil {
		_ = unix.Close(pidfd)
		return nil, err
	}
	h := &processHandles{pidfd: pidfd, mem: mem, users: 1}
	c.byGroup[tgid] = h
	return h, nil
}

// release ends one call's use of h.
func (c *targetHandles) release(h *processHandles) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h.users--
	if h.evicted && h.users == 0 {
		h.closeLocked()
	}
}

// sweepLocked evicts exited processes, and everything when all still live.
func (c *targetHandles) sweepLocked() {
	for tgid, h := range c.byGroup {
		if unix.PidfdSendSignal(h.pidfd, 0, nil, 0) != nil {
			c.evictLocked(tgid, h)
		}
	}
	if len(c.byGroup) >= maxCachedTargets {
		for tgid, h := range c.byGroup {
			c.evictLocked(tgid, h)
		}
	}
}

func (c *targetHandles) evictLocked(tgid int, h *processHandles) {
	delete(c.byGroup, tgid)
	h.evicted = true
	if h.users == 0 {
		h.closeLocked()
	}
}

func (h *processHandles) closeLocked() {
	_ = unix.Close(h.pidfd)
	h.mu.Lock()
	if h.mem != nil {
		_ = h.mem.Close()
		h.mem = nil
	}
	h.mu.Unlock()
}

// readAt reads the process's memory, reopening it once if the cached handle
// names an address space the process left by exec.
func (h *processHandles) readAt(tgid int, buf []byte, addr int64) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n, err := h.mem.ReadAt(buf, addr)
	if n > 0 || (err != nil && !isStaleAddressSpaceRead(err)) {
		return n, err
	}
	fresh, openErr := os.Open("/proc/" + strconv.Itoa(tgid) + "/mem")
	if openErr != nil {
		return n, err
	}
	_ = h.mem.Close()
	h.mem = fresh
	return h.mem.ReadAt(buf, addr)
}

func isStaleAddressSpaceRead(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, unix.EIO)
}
