package nfsdarwin

import (
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

// DisownMountForTest replaces recorded mount identity to exercise release refusal.
func (a *Adapter) DisownMountForTest(viewID, unownedPath string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	st, ok := a.views[viewID]
	if !ok || st == nil {
		return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "disown", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	m := st.mounts[st.primaryRoot]
	if m == nil {
		return &adapter.AdapterError{AdapterID: adapter.AdapterNFSDarwin, Op: "disown", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	m.mountPath = canonicalPath(unownedPath)
	m.facts.MountPath = m.mountPath
	m.facts.ExportSource = "127.0.0.1:/views/unowned/primary/fake"
	return nil
}

// SetUmountBinForTest selects unmount commands for failure and retry tests.
func (a *Adapter) SetUmountBinForTest(umountBin, diskutilBin string) {
	a.mu.Lock()
	if umountBin != "" {
		a.cfg.UmountBin = umountBin
	} else {
		a.cfg.UmountBin = DefaultUmountBin
	}
	if diskutilBin != "" {
		a.cfg.DiskutilBin = diskutilBin
	} else {
		a.cfg.DiskutilBin = DefaultDiskutilBin
	}
	a.mu.Unlock()
}
