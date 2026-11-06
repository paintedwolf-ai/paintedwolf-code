//go:build darwin

package socketref

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/lycaon/lycaon/internal/configdir"
	"golang.org/x/sys/unix"
)

func Platform() Adapter { return Adapter{Command: RetainedObject, Native: RetainedObject} }

func implicitBindingMode() Enforcement { return KernelPathname }

func setUnixAddressLength(address *unix.RawSockaddrUnix, length uint32) { address.Len = uint8(length) }

func shortReferencePrefix() string { return ".paintedwolf-socket-" + strconv.Itoa(os.Getuid()) + "-" }

// ControlPlanePathPattern protects the short address namespace before any
// reference exists, including launches that have no socket grant.
func ControlPlanePathPattern() string {
	return "(^|/)" + regexp.QuoteMeta(shortReferencePrefix()) + "[^/]+(/|$)"
}

// Open retains a hard link behind the engine's terminal control-plane floor.
// A service on another volume gets an OS-selected temporary reference there.
func Open(path string) (*Reference, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	root, err := configdir.Root()
	if err != nil {
		return nil, err
	}
	aliases := filepath.Join(root, "socket-references")
	if err := os.MkdirAll(aliases, 0700); err != nil {
		return nil, err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	realAliases, err := filepath.EvalSymlinks(aliases)
	if err != nil || realAliases != filepath.Join(realRoot, "socket-references") {
		return nil, ErrUnbound
	}
	var rootStat unix.Stat_t
	if err := unix.Lstat(realAliases, &rootStat); err != nil || rootStat.Mode&unix.S_IFMT != unix.S_IFDIR || rootStat.Mode&0777 != 0700 || rootStat.Uid != uint32(os.Getuid()) {
		return nil, ErrUnbound
	}
	aliases = realAliases

	directory, guard, releaseParent, err := captureReferenceDirectory(aliases, resolved)
	if err != nil {
		return nil, err
	}
	dir := directory
	alias := filepath.Join(dir, "socket")
	captured := false
	defer func() {
		if !captured {
			_ = os.Remove(alias)
			_ = guard.Close()
			_ = os.Remove(dir)
			_ = releaseParent()
		}
	}()
	var socketStat, dirStat unix.Stat_t
	if err := unix.Lstat(alias, &socketStat); err != nil || socketStat.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return nil, fmt.Errorf("retained local service is not a socket: %v", err)
	}
	if err := unix.Fstat(int(guard.Fd()), &dirStat); err != nil {
		return nil, err
	}
	short, releaseShort, err := shortReferenceAddress(dir)
	if err != nil {
		return nil, err
	}
	release := func() (result error) {
		defer func() { result = errors.Join(result, releaseShort(), releaseParent()) }()
		var currentDir, currentSocket unix.Stat_t
		if unix.Lstat(dir, &currentDir) != nil || currentDir.Dev != dirStat.Dev || currentDir.Ino != dirStat.Ino {
			return ErrUnbound
		}
		if err := unix.Lstat(alias, &currentSocket); err != nil {
			return err
		}
		if currentSocket.Dev != socketStat.Dev || currentSocket.Ino != socketStat.Ino {
			return ErrUnbound
		}
		return errors.Join(os.Remove(alias), os.Remove(dir))
	}
	captured = true
	return &Reference{file: guard, path: resolved, address: filepath.Join(short, "socket"), release: release, verify: newPeerBinding()}, nil
}

func newReferenceEndpoint(r *Reference, network string) (serviceEndpoint, error) {
	if network == "unixgram" {
		endpoint, err := startDatagramEndpoint(filepath.Join(filepath.Dir(r.address), "datagrams"), func(ctx context.Context, local string) (net.Conn, error) {
			return r.dialContext(ctx, network, local)
		})
		if err != nil {
			return nil, err
		}
		endpoint.remove = func() error { return unix.Unlinkat(int(r.file.Fd()), "datagrams", 0) }
		return endpoint, nil
	}
	endpoint, err := startEndpoint(filepath.Join(filepath.Dir(r.address), "relay"), r.DialContext)
	if err != nil {
		return nil, err
	}
	endpoint.listener.SetUnlinkOnClose(false)
	endpoint.remove = func() error { return unix.Unlinkat(int(r.file.Fd()), "relay", 0) }
	return endpoint, nil
}

func shortReferenceAddress(directory string) (string, func() error, error) {
	root, err := os.MkdirTemp("/private/tmp", shortReferencePrefix())
	if err != nil {
		return "", nil, err
	}
	var expected unix.Stat_t
	if err := unix.Lstat(root, &expected); err != nil || expected.Mode&unix.S_IFMT != unix.S_IFDIR || expected.Mode&0777 != 0700 || expected.Uid != uint32(os.Getuid()) {
		_ = os.Remove(root)
		return "", nil, ErrUnbound
	}
	link := filepath.Join(root, "d")
	if err := os.Symlink(directory, link); err != nil {
		_ = os.Remove(root)
		return "", nil, err
	}
	release := func() error {
		var current unix.Stat_t
		if unix.Lstat(root, &current) != nil || current.Dev != expected.Dev || current.Ino != expected.Ino {
			return ErrUnbound
		}
		target, err := os.Readlink(link)
		if err != nil || target != directory {
			return ErrUnbound
		}
		return errors.Join(os.Remove(link), os.Remove(root))
	}
	return link, release, nil
}

func descriptorPath(file *os.File) string { return filepath.Join(file.Name(), "socket") }

func duplicate(*os.File) (*os.File, error) { return nil, ErrUnbound }

func sameObject(a, b *Reference) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a == b {
		return a.file != nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if a.file == nil || b.file == nil {
		return false
	}
	var first, second unix.Stat_t
	if unix.Lstat(descriptorPath(a.file), &first) != nil || unix.Lstat(descriptorPath(b.file), &second) != nil {
		return false
	}
	return first.Dev == second.Dev && first.Ino == second.Ino
}

func matchesPath(ref *Reference, path string) bool {
	ref.mu.RLock()
	defer ref.mu.RUnlock()
	if ref.file == nil {
		return false
	}
	var held, current unix.Stat_t
	if unix.Lstat(descriptorPath(ref.file), &held) != nil || unix.Stat(path, &current) != nil {
		return false
	}
	return held.Dev == current.Dev && held.Ino == current.Ino
}
