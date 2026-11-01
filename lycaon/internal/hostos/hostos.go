// Package hostos is the platform adapter for operating-system account and
// identification facts: the kernel's uname fields, the identity the host
// process runs as, account lookups and numeric file ownership. Each platform
// declares what it provides in its own file; a fact it does not provide
// returns ErrUnsupported.
package hostos

import (
	"context"
	"errors"
	"runtime"
)

// ErrUnsupported marks a fact or capability this platform does not provide.
var ErrUnsupported = errors.New("not provided on this platform")

// ErrUnknownAccount marks a user or group name the platform does not know.
var ErrUnknownAccount = errors.New("unknown account")

// Capabilities is a platform's declared set.
type Capabilities struct {
	// System is the kernel identification reported by uname.
	System bool
	// Identity is the host process's user, group and supplementary groups.
	Identity bool
	// NumericOwnership is POSIX numeric file ownership: reading a file's
	// owner and group and resolving account names to numeric ids.
	NumericOwnership bool
}

// System holds the platform's uname fields.
type System struct {
	Sysname, Nodename, Release, Version, Machine string
}

// Identity is the effective identity of the host process. Names are empty
// where the platform does not resolve them.
type Identity struct {
	User       string
	UID, GID   int
	Groups     []int
	GroupNames map[int]string
}

// Host reads the running platform.
type Host struct{}

// Capabilities reports this platform's declared set.
func (Host) Capabilities() Capabilities { return declared }

// Processors reports the units Go schedules on: the CPU affinity set where
// the platform has one, which is also the installed count elsewhere.
func (Host) Processors(context.Context, bool) (int, error) { return runtime.NumCPU(), nil }

// System reports the kernel identification.
func (Host) System(ctx context.Context) (System, error) {
	if err := ctx.Err(); err != nil {
		return System{}, err
	}
	if !declared.System {
		return System{}, ErrUnsupported
	}
	return platformSystem()
}

// Identity reports the host process's identity.
func (Host) Identity(ctx context.Context) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	if !declared.Identity {
		return Identity{}, ErrUnsupported
	}
	return platformIdentity()
}

// CurrentIDs reports the host process's numeric user and group ids.
func (Host) CurrentIDs() (uid, gid int, err error) {
	if !declared.NumericOwnership {
		return 0, 0, ErrUnsupported
	}
	uid, gid = platformCurrentIDs()
	return uid, gid, nil
}

// LookupUser resolves a user name to its numeric id.
func (Host) LookupUser(name string) (int, error) {
	if !declared.NumericOwnership {
		return 0, ErrUnsupported
	}
	return platformLookupUser(name)
}

// LookupGroup resolves a group name to its numeric id.
func (Host) LookupGroup(name string) (int, error) {
	if !declared.NumericOwnership {
		return 0, ErrUnsupported
	}
	return platformLookupGroup(name)
}

// FileOwner reports the numeric owner and group of path without following a
// final symbolic link.
func (Host) FileOwner(path string) (uid, gid int, err error) {
	if !declared.NumericOwnership {
		return 0, 0, ErrUnsupported
	}
	return platformFileOwner(path)
}
