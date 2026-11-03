//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestNestedPinEpochFixture(t *testing.T) {
	if os.Getenv("PW_NESTED_PIN_EPOCH_FIXTURE") == "" {
		return
	}
	if err := checkNestedPinEpoch(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func TestNestedPinCaptureDuringSiblingPublication(t *testing.T) {
	command := exec.Command("sudo", "-n", "env", "PW_NESTED_PIN_EPOCH_FIXTURE=1", os.Args[0], "-test.run=^TestNestedPinEpochFixture$")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("owned sibling mount capture: %v\n%s", err, out)
	}
}

func checkNestedPinEpoch() error {
	if err := enterOwnedPrivateTopologyNamespace(); err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "pw-nested-pin-epoch-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	target, sibling := filepath.Join(root, "target"), filepath.Join(root, "sibling")
	for _, path := range []string{target, sibling} {
		if err := os.Mkdir(path, 0700); err != nil {
			return err
		}
	}
	if err := unix.Mount("none", target, "tmpfs", unix.MS_NODEV|unix.MS_NOSUID|unix.MS_NOEXEC, "size=64k"); err != nil {
		return err
	}
	defer unix.Unmount(target, unix.MNT_DETACH)
	selected, err := openMountPoint(target)
	if err != nil {
		return err
	}
	defer unix.Close(selected)
	id, inode, err := mountDescriptorIdentity(selected)
	if err != nil {
		return err
	}
	fd, changed, err := captureNestedViewPin(target, int(id), func(info int, buf []byte) ([]byte, error) {
		if err := unix.Mount("none", sibling, "tmpfs", unix.MS_NODEV|unix.MS_NOSUID|unix.MS_NOEXEC, "size=64k"); err != nil {
			return nil, err
		}
		return readMountInfo(info, buf)
	})
	defer unix.Unmount(sibling, unix.MNT_DETACH)
	if fd >= 0 {
		_ = unix.Close(fd)
	}
	if err != nil || !changed || fd >= 0 {
		return fmt.Errorf("sibling publication was not discarded: fd=%d changed=%t err=%v", fd, changed, err)
	}
	fd, changed, err = captureNestedViewPin(target, int(id), readMountInfo)
	if err != nil || changed || fd < 0 {
		return fmt.Errorf("fresh selected pin unavailable: fd=%d changed=%t err=%v", fd, changed, err)
	}
	defer unix.Close(fd)
	actualID, actualInode, err := mountDescriptorIdentity(fd)
	if err != nil || actualID != id || actualInode != inode {
		return errors.Join(err, errors.New("fresh capture changed the selected mount object"))
	}
	for _, check := range []struct {
		id   int
		read func(int, []byte) ([]byte, error)
	}{
		{int(id) + 1, readMountInfo},
		{int(id), func(int, []byte) ([]byte, error) { return []byte("invalid\n"), nil }},
	} {
		refused, changed, err := captureNestedViewPin(target, check.id, check.read)
		if refused >= 0 {
			_ = unix.Close(refused)
		}
		if err == nil || changed || refused >= 0 {
			return errors.New("stable identity/topology failure gained retry or a pin")
		}
	}
	return nil
}

func TestNestedPinCaptureReadErrorRemainsFatal(t *testing.T) {
	fd, changed, err := captureNestedViewPin("/", 1, func(int, []byte) ([]byte, error) { return nil, unix.EIO })
	if fd >= 0 {
		testutil.FailErr(t, "close unexpected read-error pin", unix.Close(fd))
	}
	if fd >= 0 || changed || !errors.Is(err, unix.EIO) {
		t.Fatalf("read error gained retry or a pin: fd=%d changed=%t err=%v", fd, changed, err)
	}
}
