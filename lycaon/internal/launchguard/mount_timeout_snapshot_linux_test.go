//go:build linux

package launchguard

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

var errFixtureMountVisibilityDeadline = errors.New("safe volume did not become visible")

type timeoutMountRow struct {
	ID         int    `json:"id"`
	Parent     int    `json:"parent"`
	Device     string `json:"device"`
	Root       string `json:"root"`
	Point      string `json:"mount_point"`
	Filesystem string `json:"filesystem"`
	Options    string `json:"options"`
	Shared     int    `json:"shared"`
	Master     int    `json:"master"`
}

type timeoutMountNamespace struct {
	Name            string            `json:"name"`
	BeginNS         int64             `json:"begin_ns"`
	EndNS           int64             `json:"end_ns"`
	NamespaceDevice uint64            `json:"namespace_device"`
	NamespaceInode  uint64            `json:"namespace_inode"`
	MountID         uint64            `json:"mount_id"`
	Inode           uint64            `json:"inode"`
	Flags           int64             `json:"flags"`
	TargetErrno     int               `json:"target_errno"`
	LateFileType    uint32            `json:"late_file_type"`
	LateFileErrno   int               `json:"late_file_errno"`
	Rows            []timeoutMountRow `json:"rows"`
}

// The module-view fixture already owns a locked OS thread with CLONE_FS
// unshared. Only this short-lived worker enters these captured namespaces.
func recordMountVisibilityTimeout(host *os.File, target, lateFile string) {
	result := struct {
		BeginNS    int64                   `json:"begin_ns"`
		EndNS      int64                   `json:"end_ns"`
		Target     string                  `json:"target"`
		Roles      []lifecycleRole         `json:"roles,omitempty"`
		Namespaces []timeoutMountNamespace `json:"namespaces,omitempty"`
		Error      string                  `json:"error,omitempty"`
	}{BeginNS: lifecycleMonotonicNS(), Target: target}
	err := func() (err error) {
		owned, err := captureFixtureLifecyclePins()
		if err != nil {
			return err
		}
		defer owned.close()
		result.Roles = owned.roles
		current, err := os.Open("/proc/thread-self/ns/mnt")
		if err != nil {
			return err
		}
		defer current.Close()
		defer func() { err = errors.Join(err, unix.Setns(int(current.Fd()), unix.CLONE_NEWNS)) }()
		for _, selection := range []struct {
			name  string
			index int
		}{{"host", -1}, {"intake", 2}, {"desktop", 0}} {
			namespace := host
			if selection.index >= 0 {
				role := owned.roles[selection.index]
				namespace, err = os.Open(fmt.Sprintf("/proc/%d/ns/mnt", role.PID))
				if err != nil {
					return err
				}
			}
			if selection.index >= 0 {
				if err := recheckTimeoutRole(owned.roles[selection.index], owned.pins[selection.index], namespace); err != nil {
					_ = namespace.Close()
					return err
				}
			}
			row, observeErr := observeTimeoutMountNamespace(selection.name, namespace, target, lateFile)
			if selection.index >= 0 {
				role := owned.roles[selection.index]
				if observeErr == nil {
					observeErr = recheckTimeoutRole(role, owned.pins[selection.index], namespace)
				}
				_ = namespace.Close()
			}
			result.Namespaces = append(result.Namespaces, row)
			if observeErr != nil {
				return observeErr
			}
		}
		after, err := captureFixtureLifecyclePins()
		if err != nil {
			return err
		}
		defer after.close()
		if !slices.Equal(owned.roles, after.roles) {
			return errors.New("timeout snapshot selected role bindings changed")
		}
		return nil
	}()
	if err != nil {
		result.Error = err.Error()
		if len(result.Error) > 512 {
			result.Error = result.Error[:512]
		}
	}
	result.EndNS = lifecycleMonotonicNS()
	raw, err := json.Marshal(result)
	if err == nil && len(raw) <= 16384 {
		fmt.Printf("PW_MOUNT_TIMEOUT01 %s\n", raw)
	}
}

func recheckTimeoutRole(role lifecycleRole, pin int, namespace *os.File) error {
	actual, err := lifecycleBirth(role.PID)
	actual.Role = role.Role
	if err != nil || actual != role {
		return errors.New("timeout snapshot role birth changed")
	}
	polls := []unix.PollFd{{Fd: int32(pin), Events: unix.POLLIN}}
	if _, err := unix.Poll(polls, 0); err != nil {
		return err
	}
	if polls[0].Revents != 0 {
		return errors.New("timeout snapshot role exited during observation")
	}
	actualNS, err := os.Stat(fmt.Sprintf("/proc/%d/ns/mnt", role.PID))
	if err != nil {
		return err
	}
	capturedNS, err := namespace.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(actualNS, capturedNS) {
		return errors.New("timeout snapshot role namespace changed")
	}
	return nil
}

func observeTimeoutMountNamespace(name string, namespace *os.File, target, lateFile string) (row timeoutMountNamespace, err error) {
	row.Name, row.BeginNS = name, lifecycleMonotonicNS()
	defer func() { row.EndNS = lifecycleMonotonicNS() }()
	var ns unix.Stat_t
	if err := unix.Fstat(int(namespace.Fd()), &ns); err != nil {
		return row, err
	}
	row.NamespaceDevice, row.NamespaceInode = uint64(ns.Dev), ns.Ino
	if err := unix.Setns(int(namespace.Fd()), unix.CLONE_NEWNS); err != nil {
		return row, err
	}
	_, entries, err := readCurrentMountTopology()
	if err != nil {
		return row, err
	}
	selected := make(map[int]bool)
	fd, openErr := openMountPoint(target)
	if openErr != nil {
		row.TargetErrno = timeoutSyscallErrno(openErr)
	} else {
		defer unix.Close(fd)
		row.MountID, row.Inode, err = mountDescriptorIdentity(fd)
		if err != nil {
			return row, err
		}
		var fs unix.Statfs_t
		if err := unix.Fstatfs(fd, &fs); err != nil {
			return row, err
		}
		row.Flags = fs.Flags
		selected[int(row.MountID)] = true
	}
	for _, entry := range entries {
		if entry.mountPoint == target {
			selected[entry.id] = true
		}
	}
	if len(selected) > 4 {
		return row, errors.New("timeout snapshot selected alias exceeds its row bound")
	}
	parents := make(map[int]bool)
	for _, entry := range entries {
		if selected[entry.id] {
			parents[entry.parent] = true
		}
	}
	for _, entry := range entries {
		if selected[entry.id] || parents[entry.id] {
			row.Rows = append(row.Rows, timeoutMountRow{entry.id, entry.parent, entry.device, entry.root, entry.mountPoint,
				entry.filesystem, entry.options, entry.shared, entry.master})
		}
	}
	if len(row.Rows) > 8 {
		return row, errors.New("timeout snapshot immediate parents exceed their row bound")
	}
	var file unix.Stat_t
	if statErr := unix.Lstat(lateFile, &file); statErr != nil {
		row.LateFileErrno = timeoutSyscallErrno(statErr)
	} else {
		row.LateFileType = file.Mode & unix.S_IFMT
	}
	return row, nil
}

func timeoutSyscallErrno(err error) int {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return int(errno)
	}
	return -1
}

func TestMountTimeoutSnapshotFixture(t *testing.T) {
	if os.Getenv("PW_MOUNT_TIMEOUT_SNAPSHOT_FIXTURE") == "" {
		return
	}
	runtime.LockOSThread()
	if os.Geteuid() != 0 {
		t.Fatal("mount snapshot fixture requires initial root")
	}
	if err := unix.Unshare(unix.CLONE_FS | unix.CLONE_NEWNS); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	target := filepath.Join(root, "owned-volume")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("none", target, "tmpfs", unix.MS_NODEV|unix.MS_NOSUID|unix.MS_NOEXEC, "size=1m"); err != nil {
		t.Fatal(err)
	}
	defer unix.Unmount(target, unix.MNT_DETACH)
	file := filepath.Join(target, "late.so")
	if err := os.WriteFile(file, []byte("owned"), 0600); err != nil {
		t.Fatal(err)
	}
	ns, err := os.Open("/proc/thread-self/ns/mnt")
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Close()
	present, err := observeTimeoutMountNamespace("owned", ns, target, file)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := openMountPoint(target)
	if err != nil {
		t.Fatal(err)
	}
	id, inode, err := mountDescriptorIdentity(fd)
	_ = unix.Close(fd)
	if err != nil {
		t.Fatal(err)
	}
	if present.MountID != id || present.Inode != inode || present.LateFileType != unix.S_IFREG || present.LateFileErrno != 0 ||
		present.Flags&unix.ST_NOEXEC == 0 || present.NamespaceInode == 0 || present.EndNS < present.BeginNS || len(present.Rows) != 2 {
		t.Fatalf("snapshot lost actual owned namespace/mount/file facts: %+v", present)
	}
	if err := unix.Unmount(target, 0); err != nil {
		t.Fatal(err)
	}
	absent, err := observeTimeoutMountNamespace("underlying", ns, target, file)
	if err != nil {
		t.Fatal(err)
	}
	if absent.MountID == present.MountID || absent.LateFileErrno != int(unix.ENOENT) || absent.LateFileType != 0 {
		t.Fatalf("snapshot confused disappeared source and underlying mount: %+v", absent)
	}
	raw, err := json.Marshal([]timeoutMountNamespace{present, absent})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("PW_OWNED_TIMEOUT_SNAPSHOT01 %s\n", raw)
}

func TestMountTimeoutSnapshotBindsActualOwnedNamespaceAndMount(t *testing.T) {
	command := exec.Command("sudo", "-n", "env", "PW_MOUNT_TIMEOUT_SNAPSHOT_FIXTURE=1", os.Args[0], "-test.run=^TestMountTimeoutSnapshotFixture$")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("owned namespace snapshot fixture: %v\n%s", err, out)
	}
	t.Logf("actual owned timeout snapshot facts: %s", out)
}
