//go:build linux

package fuselinux

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

const reclaimUnmountBound = 5 * time.Second

var _ adapter.ConnectionRecovery = (*Adapter)(nil)

// SettleRecordedConnection removes a mount an earlier engine process left behind, identified
// by its recorded source and device. A connection that already ended only needs detaching.
// A live connection whose recorded server is proven dead can only be held open by that
// server's own stuck thread, so its kernel abort file ends it first. Nothing is signaled:
// holders of the mount see ENOTCONN and keep running.
func (a *Adapter) SettleRecordedConnection(ctx context.Context, rec adapter.RecordedConnection) (adapter.ConnectionSettlement, error) {
	if a.cfg.HostOS != "linux" {
		return adapter.ConnectionSettlement{Disposition: adapter.ConnectionAbsent, Diagnostic: "no FUSE mounts exist on this host"}, nil
	}
	entry, state, err := findRecordedMount(rec)
	if err != nil {
		return adapter.ConnectionSettlement{Disposition: adapter.ConnectionUnresolved, Diagnostic: err.Error()}, err
	}
	switch state {
	case recordedMountAbsent:
		return adapter.ConnectionSettlement{Disposition: adapter.ConnectionAbsent}, nil
	case recordedMountForeign:
		return adapter.ConnectionSettlement{
			Disposition: adapter.ConnectionForeign,
			Diagnostic:  fmt.Sprintf("mount at %s has source %q device %q, recorded %q %q", rec.MountPath, entry.source, entry.devMajorMinor, rec.FSName, rec.DevMajorMinor),
		}, nil
	case recordedMountMatches:
	}
	aborted := false
	if !connectionEnded(ctx, entry.mountPoint) {
		if !servingProcessEnded(rec.Server) {
			return adapter.ConnectionSettlement{Disposition: adapter.ConnectionServerAlive, Diagnostic: fmt.Sprintf("server %d of %s is not proven dead", rec.Server.PID, rec.MountPath)}, nil
		}
		if rec.DevMajorMinor == "" {
			return adapter.ConnectionSettlement{Disposition: adapter.ConnectionUnresolved, Diagnostic: "no connection identity was recorded for " + rec.MountPath}, nil
		}
		if err := abortConnection(rec.DevMajorMinor); err != nil {
			return adapter.ConnectionSettlement{Disposition: adapter.ConnectionUnresolved, Diagnostic: err.Error()}, err
		}
		aborted = true
		if !connectionEnded(ctx, entry.mountPoint) {
			return adapter.ConnectionSettlement{Disposition: adapter.ConnectionUnresolved, ConnectionAborted: true, Diagnostic: "connection still answers after abort on " + rec.MountPath}, nil
		}
	}
	unmountCtx, cancel := context.WithTimeout(ctx, reclaimUnmountBound)
	defer cancel()
	a.mu.RLock()
	fusermountBin := a.cfg.FusermountBin
	a.mu.RUnlock()
	if err := runFusermountUnmount(unmountCtx, fusermountBin, entry.mountPoint, true); err != nil {
		return adapter.ConnectionSettlement{Disposition: adapter.ConnectionUnresolved, ConnectionAborted: aborted, Diagnostic: err.Error()}, err
	}
	_, after, err := findRecordedMount(rec)
	if err != nil || after != recordedMountAbsent {
		diagnostic := "mount remains after detach: " + rec.MountPath
		return adapter.ConnectionSettlement{Disposition: adapter.ConnectionUnresolved, ConnectionAborted: aborted, Diagnostic: diagnostic}, err
	}
	return adapter.ConnectionSettlement{Disposition: adapter.ConnectionDetached, ConnectionAborted: aborted}, nil
}

type recordedMountState int

const (
	recordedMountAbsent recordedMountState = iota
	recordedMountMatches
	recordedMountForeign
)

func recordMountFacts(rec *adapter.RecordedConnection, facts MountFacts) {
	rec.MountID = facts.MountID
	rec.ConnectionID = facts.ConnectionID
	rec.DevMajorMinor = facts.DevMajorMinor
}

func newConnectionRecord(viewID, rootID, mountPath, fsName string) (adapter.RecordedConnection, error) {
	server, err := adapter.CurrentProcessIdentity(os.Getpid())
	if err != nil {
		return adapter.RecordedConnection{}, err
	}
	return adapter.RecordedConnection{
		AdapterID: adapter.AdapterFUSELinux, ViewID: viewID, RootID: rootID,
		MountPath: mountPath, FSName: fsName, OwnerUID: uint32(os.Getuid()), Server: server,
	}, nil
}
