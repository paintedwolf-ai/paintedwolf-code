package fuselinux

import (
	"os/exec"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

// DisownMountForTest rewrites the recorded mount identity for viewID so tests can verify that
// Release refuses to unmount a path that no longer matches it.
func (a *Adapter) DisownMountForTest(viewID, unownedPath string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	st, ok := a.views[viewID]
	if !ok || st == nil {
		return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "disown", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	m := st.mounts[st.primaryRoot]
	m.mountPath = canonicalPath(unownedPath)
	m.facts.MountPath = m.mountPath
	m.facts.FSName = "lycaon-unowned-fake"
	return nil
}

// ForgeMountDeviceForTest records another connection's device number while keeping the path and source.
func (a *Adapter) ForgeMountDeviceForTest(viewID, dev string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.views[viewID]
	st.mounts[st.primaryRoot].facts.DevMajorMinor = dev
}

// SetFusermountBinForTest updates FusermountBin so tests can inject unmount failures and restore them.
func (a *Adapter) SetFusermountBinForTest(fusermountBin string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if fusermountBin != "" {
		a.cfg.FusermountBin = fusermountBin
	} else if resolved, err := exec.LookPath("fusermount3"); err == nil && resolved != "" {
		a.cfg.FusermountBin = resolved
	} else {
		a.cfg.FusermountBin = DefaultFusermountBin
	}
}

// SetConfigCallbacksForTest updates the fault-injection callbacks for client sync and runtime cleanup.
func (a *Adapter) SetConfigCallbacksForTest(clientSyncFn func(string) error, removeAllFn func(string) error) {
	a.mu.Lock()
	a.cfg.ClientSyncFn = clientSyncFn
	a.cfg.RemoveAllFn = removeAllFn
	a.mu.Unlock()
}

// KillServerForView closes the FUSE server for viewID to reproduce a mid-session server stop.
func (a *Adapter) KillServerForView(viewID string) error {
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok || st == nil {
		return &adapter.AdapterError{AdapterID: adapter.AdapterFUSELinux, Op: "kill_server", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	for rootID, m := range st.mounts {
		m.serverDead.Store(true)
		_ = m.server.Close()
		if st.privateGitMgr != nil {
			st.privateGitMgr.RevokeMountedViewBinding(viewID, rootID)
		}
	}
	return nil
}

// LockWaiterCountForTest reports how many requests currently hold a lock waiter slot.
func (s *Server) LockWaiterCountForTest() int {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	return s.lockWaiterCount
}

// GatedMutationsForTest reports how many mutating requests are held by the barrier gate.
func (s *Server) GatedMutationsForTest() int64 {
	s.gateMu.Lock()
	defer s.gateMu.Unlock()
	return s.gatedMut
}

// PollRequestsForTest reports how many FUSE_POLL requests have reached the server.
func (s *Server) PollRequestsForTest() int64 { return s.pollRequests.Load() }
