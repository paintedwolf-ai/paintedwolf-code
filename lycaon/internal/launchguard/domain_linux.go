//go:build linux

package launchguard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const domainRoot = "/sys/fs/cgroup/paintedwolf"

// Domain retains the complete owned descendant tree without changing host PID
// semantics. Its parent is provisioned root-only by installation integration.
type Domain struct {
	parent    *os.File
	directory *os.File
	events    *os.File
	name      string
}

func CreateDomain() (*Domain, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("launch domain requires privileged installation authority")
	}
	parent, err := openRootControlled(domainRoot, true)
	if err != nil {
		return nil, err
	}
	var fs unix.Statfs_t
	if err = unix.Fstatfs(int(parent.Fd()), &fs); err != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		_ = parent.Close()
		return nil, errors.New("launch domain is not a cgroup v2 hierarchy")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		_ = parent.Close()
		return nil, err
	}
	name := "launch-" + hex.EncodeToString(nonce[:])
	if err := unix.Mkdirat(int(parent.Fd()), name, 0700); err != nil {
		_ = parent.Close()
		return nil, err
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_RDONLY, 0)
	if err != nil {
		_ = unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
		_ = parent.Close()
		return nil, err
	}
	domain := &Domain{parent: parent, directory: os.NewFile(uintptr(fd), "owned-launch-domain"), name: name}
	eventFD, err := unix.Openat(fd, "cgroup.events", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = domain.Close()
		return nil, err
	}
	domain.events = os.NewFile(uintptr(eventFD), "launch-domain-events")
	return domain, nil
}

// Attach requires clone3's atomic cgroup placement, before the child can run.
func (d *Domain) Attach(attributes *syscall.SysProcAttr) error {
	if d == nil || d.directory == nil || attributes == nil {
		return errors.New("missing launch domain")
	}
	attributes.UseCgroupFD = true
	attributes.CgroupFD = int(d.directory.Fd())
	return nil
}

func (d *Domain) populated() (bool, error) {
	if d.events == nil {
		return false, errors.New("missing launch domain events")
	}
	raw := make([]byte, 4097)
	n, err := d.events.ReadAt(raw, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	if n > 4096 {
		return false, errors.New("launch domain events exceed their bound")
	}
	return populatedState(string(raw[:n]))
}

func populatedState(raw string) (bool, error) {
	found := false
	populated := false
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return false, errors.New("invalid launch domain event")
		}
		if fields[0] != "populated" {
			continue
		}
		if found {
			return false, errors.New("duplicate launch domain population")
		}
		value, err := strconv.Atoi(fields[1])
		if err != nil || (value != 0 && value != 1) {
			return false, errors.New("invalid launch domain population")
		}
		found = true
		populated = value == 1
	}
	if !found {
		return false, errors.New("missing launch domain population")
	}
	return populated, nil
}

// WaitEmpty keeps generation authority alive until every owned descendant has
// exited, including children that outlive the primary desktop process.
func (d *Domain) WaitEmpty(ctx context.Context) error {
	for {
		populated, err := d.populated()
		if err != nil {
			return err
		}
		if !populated {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		descriptors := []unix.PollFd{{Fd: int32(d.events.Fd()), Events: unix.POLLPRI | unix.POLLERR}}
		if _, err := unix.Poll(descriptors, 1000); err != nil && !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

// Terminate uses the kernel's fork- and migration-safe operation, exclusively
// on this launch's retained cgroup. It never searches for process names.
func (d *Domain) Terminate() error {
	if d == nil || d.directory == nil {
		return errors.New("missing launch domain")
	}
	fd, err := unix.Openat(int(d.directory.Fd()), "cgroup.kill", unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "owned-domain-kill")
	defer func() { _ = file.Close() }()
	_, err = file.WriteString("1")
	return err
}

// Close refuses to release a populated domain. The supervisor must retain its
// generation lock until WaitEmpty succeeds, including on startup failures.
func (d *Domain) Close() error {
	if d.events != nil {
		populated, err := d.populated()
		if err != nil {
			return err
		}
		if populated {
			return errors.New("launch domain still owns live processes")
		}
		_ = d.events.Close()
		d.events = nil
	}
	if d.directory != nil {
		_ = d.directory.Close()
		d.directory = nil
	}
	if d.parent != nil {
		if err := unix.Unlinkat(int(d.parent.Fd()), d.name, unix.AT_REMOVEDIR); err != nil {
			return fmt.Errorf("remove empty owned domain: %w", err)
		}
		_ = d.parent.Close()
		d.parent = nil
	}
	return nil
}
