//go:build darwin

package nfsdarwin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

var _ adapter.ConnectionRecovery = (*Adapter)(nil)

// SettleRecordedConnection removes a mount an earlier engine process left behind. The export source
// carries a capability no later server reissues, so a mount table entry with that source can only be
// the recorded connection. Holders see their I/O fail and are never signaled.
func (a *Adapter) SettleRecordedConnection(ctx context.Context, rec adapter.RecordedConnection) (adapter.ConnectionSettlement, error) {
	if a.cfg.HostOS != "darwin" {
		return adapter.ConnectionSettlement{Disposition: adapter.ConnectionAbsent, Diagnostic: "no NFS view mounts exist on this host"}, nil
	}
	state, diagnostic, err := findRecordedMount(rec)
	if err != nil {
		return adapter.ConnectionSettlement{Disposition: adapter.ConnectionUnresolved, Diagnostic: err.Error()}, err
	}
	switch state {
	case recordedMountAbsent:
		return adapter.ConnectionSettlement{Disposition: adapter.ConnectionAbsent}, nil
	case recordedMountForeign:
		return adapter.ConnectionSettlement{Disposition: adapter.ConnectionForeign, Diagnostic: diagnostic}, nil
	case recordedMountMatches:
	}
	if !rec.Server.Ended() {
		return adapter.ConnectionSettlement{Disposition: adapter.ConnectionServerAlive, Diagnostic: fmt.Sprintf("server %d of %s is not proven dead", rec.Server.PID, rec.MountPath)}, nil
	}
	return a.forceRemoveRecordedMount(ctx, rec)
}

func (a *Adapter) forceRemoveRecordedMount(ctx context.Context, rec adapter.RecordedConnection) (adapter.ConnectionSettlement, error) {
	a.mu.RLock()
	umountBin, diskutilBin := a.cfg.UmountBin, a.cfg.DiskutilBin
	a.mu.RUnlock()
	mountPath := filepath.Clean(rec.MountPath)
	removed := func() (bool, error) {
		state, _, err := findRecordedMount(rec)
		return err == nil && state == recordedMountAbsent, err
	}
	uCtx, cancel := context.WithTimeout(ctx, unmountWaitBound)
	out, uErr := runBoundedHelper(uCtx, "nfs_unmount", umountBin, "-f", mountPath)
	cancel()
	if gone, err := removed(); err != nil || gone {
		return settledRemoval(gone, err)
	}
	dCtx, dCancel := context.WithTimeout(ctx, unmountWaitBound)
	dOut, dErr := runBoundedHelper(dCtx, "nfs_unmount", diskutilBin, "unmount", "force", mountPath)
	dCancel()
	if gone, err := removed(); err != nil || gone {
		return settledRemoval(gone, err)
	}
	return adapter.ConnectionSettlement{
		Disposition: adapter.ConnectionUnresolved,
		Diagnostic:  fmt.Sprintf("mount remains at %s: umount=%v (%s) diskutil=%v (%s)", mountPath, uErr, strings.TrimSpace(string(out)), dErr, strings.TrimSpace(string(dOut))),
	}, nil
}

func settledRemoval(gone bool, err error) (adapter.ConnectionSettlement, error) {
	if err != nil || !gone {
		return adapter.ConnectionSettlement{Disposition: adapter.ConnectionUnresolved, Diagnostic: fmt.Sprint(err)}, err
	}
	return adapter.ConnectionSettlement{Disposition: adapter.ConnectionDetached}, nil
}

type recordedMountState int

const (
	recordedMountAbsent recordedMountState = iota
	recordedMountMatches
	recordedMountForeign
)

// mountTableEntry is one row of the kernel's mount table, read without touching the mount, so a
// dead or wedged server cannot hold the caller.
type mountTableEntry struct {
	onName, fromName, fsType string
	owner                    uint32
	device                   int32
}

// findRecordedMount requires every mount at the recorded path to be the recorded one. Anything stacked
// there is foreign: unmounting by path would remove the topmost, whichever that is.
func findRecordedMount(rec adapter.RecordedConnection) (recordedMountState, string, error) {
	table, err := mountTableEntries()
	if err != nil {
		return recordedMountAbsent, "", err
	}
	mountPath := filepath.Clean(rec.MountPath)
	state := recordedMountAbsent
	for _, entry := range table {
		if filepath.Clean(entry.onName) != mountPath {
			continue
		}
		if entry.fsType != "nfs" || entry.fromName != rec.FSName || entry.owner != rec.OwnerUID || !deviceMatches(rec.DevMajorMinor, entry.device) {
			return recordedMountForeign, fmt.Sprintf("mount at %s has source %q type %q owner %d, recorded %q owner %d", mountPath, entry.fromName, entry.fsType, entry.owner, rec.FSName, rec.OwnerUID), nil
		}
		state = recordedMountMatches
	}
	return state, "", nil
}

func deviceMatches(recorded string, device int32) bool {
	return recorded == "" || device == 0 || recorded == formatDevice(device)
}

func formatDevice(device int32) string {
	return strconv.Itoa(int(device>>24)&0xff) + ":" + strconv.Itoa(int(device)&0xffffff)
}

func newConnectionRecord(viewID, rootID, mountPath, exportSource string) (adapter.RecordedConnection, error) {
	server, err := adapter.CurrentProcessIdentity(os.Getpid())
	if err != nil {
		return adapter.RecordedConnection{}, err
	}
	return adapter.RecordedConnection{
		AdapterID: adapter.AdapterNFSDarwin, ViewID: viewID, RootID: rootID,
		MountPath: mountPath, FSName: exportSource, OwnerUID: uint32(os.Getuid()), Server: server,
	}, nil
}
