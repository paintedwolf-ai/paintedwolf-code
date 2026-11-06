//go:build darwin

package testvolume

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	lexec "github.com/lycaon/lycaon/internal/exec"
)

const hdiutilPath = "/usr/bin/hdiutil"

// New attaches a fresh disk image. A host without hdiutil cannot run these tests.
// The second result is true when the caller should return; it never is on macOS.
func New(t *testing.T, kind Kind) (*Volume, bool) {
	t.Helper()
	if _, err := os.Stat(hdiutilPath); err != nil {
		t.Skipf("hdiutil unavailable: %v", err)
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temporary directory: %v", err)
	}
	image, mount := placementPaths(base)
	if err := os.Mkdir(mount, 0o750); err != nil {
		t.Fatalf("create mount point: %v", err)
	}
	hdiutil(t, "create", "-size", "32m", "-fs", filesystemName(kind), "-volname", filepath.Base(mount), "-quiet", image)
	attach := func(t *testing.T) {
		hdiutil(t, "attach", image, "-nobrowse", "-owners", "on", "-mountpoint", mount, "-quiet")
	}
	attached := false
	v := &Volume{Root: mount, image: image}
	v.eject = func(t *testing.T) {
		hdiutil(t, "detach", mount, "-quiet")
		attached = false
	}
	v.remount = func(t *testing.T) {
		attach(t)
		attached = true
	}
	v.Remount(t)
	t.Cleanup(func() {
		if attached {
			if out, err := runHdiutil("detach", mount, "-force", "-quiet"); err != nil {
				t.Errorf("detach test volume: %v: %s", err, out)
			}
		}
	})
	return v, false
}

var (
	sharedMu     sync.Mutex
	sharedMounts = map[Kind]*Volume{}
)

// sharedVolume returns the one volume of a kind for this test binary, attaching
// it on first use. Movable shares the Clonable image: on macOS they are the same
// filesystem.
func sharedVolume(t *testing.T, kind Kind) (*Volume, bool) {
	t.Helper()
	if _, err := os.Stat(hdiutilPath); err != nil {
		t.Skipf("hdiutil unavailable: %v", err)
	}
	if kind != NoClone {
		kind = Clonable
	}
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if v, ok := sharedMounts[kind]; ok {
		return v, false
	}
	v, err := attachSharedVolume(kind)
	if err != nil {
		t.Fatalf("attach shared test volume: %v", err)
	}
	sharedMounts[kind] = v
	return v, false
}

func attachSharedVolume(kind Kind) (*Volume, error) {
	base, err := os.MkdirTemp("", "pwplacement-") //nolint:usetesting // the mount must outlive every test in the binary
	if err != nil {
		return nil, err
	}
	attached := false
	defer func() {
		if !attached {
			_ = os.RemoveAll(base)
		}
	}()
	image, mount := placementPaths(base)
	if err := os.Mkdir(mount, 0o750); err != nil {
		return nil, err
	}
	if out, err := runHdiutil("create", "-size", "32m", "-fs", filesystemName(kind), "-volname", filepath.Base(mount), "-quiet", image); err != nil {
		return nil, fmt.Errorf("create: %v: %s", err, out)
	}
	if out, err := runHdiutil("attach", image, "-nobrowse", "-owners", "on", "-mountpoint", mount, "-quiet"); err != nil {
		return nil, fmt.Errorf("attach: %v: %s", err, out)
	}
	attached = true
	return &Volume{Root: mount, image: image, shared: true, eject: sharedLifecycleFatal, remount: sharedLifecycleFatal}, nil
}

// placementPaths names the image and its mount point after the process, so any
// leftover is greppable and obviously a test artifact.
func placementPaths(base string) (image, mount string) {
	name := fmt.Sprintf("pwplacement-%d", os.Getpid())
	return filepath.Join(base, name+".dmg"), filepath.Join(base, name)
}

func filesystemName(kind Kind) string {
	if kind == NoClone {
		return "HFS+"
	}
	return "APFS"
}

// sweepStrays detaches pwplacement volumes of gone processes, and this finishing
// process, that hdiutil still reports, then removes their images.
func sweepStrays() {
	out, err := runHdiutil("info")
	if err != nil {
		return // without a listing the sweep must not guess
	}
	mounts, images := strayVolumes(string(out), processGone)
	for _, mount := range mounts {
		_, _ = runHdiutil("detach", mount, "-force", "-quiet")
	}
	for _, image := range images {
		_ = os.Remove(image)
		_ = os.RemoveAll(filepath.Dir(image))
	}
}

// strayVolumes picks pwplacement-<pid> images and their mount points out of
// hdiutil info output for processes that are gone.
func strayVolumes(info string, gone func(int) bool) (mounts, images []string) {
	var (
		image  string
		points []string
	)
	flush := func() {
		if pid, ok := strayPID(image, points); ok && gone(pid) {
			mounts = append(mounts, points...)
			if image != "" {
				images = append(images, image)
			}
		}
		image, points = "", nil
	}
	for _, line := range strings.Split(info, "\n") {
		if path, ok := imageLine(line); ok {
			flush()
			image = path
			continue
		}
		if mount, ok := mountLine(line); ok {
			points = append(points, mount)
		}
	}
	flush()
	return mounts, images
}

func imageLine(line string) (string, bool) {
	field, ok := strings.CutPrefix(strings.TrimSpace(line), "image-path")
	if !ok {
		return "", false
	}
	_, value, ok := strings.Cut(field, ":")
	if !ok {
		return "", false
	}
	value = strings.TrimSpace(value)
	return value, value != ""
}

func mountLine(line string) (string, bool) {
	fields := strings.Split(line, "\t")
	if len(fields) < 3 || !strings.HasPrefix(fields[0], "/dev/") {
		return "", false
	}
	mount := strings.TrimSpace(fields[len(fields)-1])
	return mount, strings.HasPrefix(mount, "/")
}

func strayPID(image string, points []string) (int, bool) {
	if pid, ok := pidFromName(filepath.Base(image)); ok {
		return pid, true
	}
	for _, point := range points {
		if pid, ok := pidFromName(filepath.Base(point)); ok {
			return pid, true
		}
	}
	return 0, false
}

func pidFromName(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, "pwplacement-")
	if !ok {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSuffix(rest, ".dmg"))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// processGone reports whether a pid is this finishing process or no longer runs.
func processGone(pid int) bool {
	if pid == os.Getpid() {
		return true
	}
	switch unix.Kill(pid, 0) {
	case unix.ESRCH:
		return true
	case nil, unix.EPERM:
		return false
	default:
		return false
	}
}

// RemountIgnoringOwnership mounts the volume the way external disks commonly are.
func (v *Volume) RemountIgnoringOwnership(t *testing.T) {
	t.Helper()
	v.Eject(t)
	hdiutil(t, "attach", v.image, "-nobrowse", "-owners", "off", "-mountpoint", v.Root, "-quiet")
	t.Cleanup(func() { _, _ = runHdiutil("detach", v.Root, "-force", "-quiet") })
}

// MakeReadOnly remounts the volume read-only.
func (v *Volume) MakeReadOnly(t *testing.T) {
	t.Helper()
	v.Eject(t)
	hdiutil(t, "attach", v.image, "-nobrowse", "-readonly", "-owners", "on", "-mountpoint", v.Root, "-quiet")
	t.Cleanup(func() { _, _ = runHdiutil("detach", v.Root, "-force", "-quiet") })
}

func hdiutil(t *testing.T, args ...string) {
	t.Helper()
	if out, err := runHdiutil(args...); err != nil {
		t.Fatalf("hdiutil %v: %v: %s", args, err, out)
	}
}

func runHdiutil(args ...string) ([]byte, error) {
	out, code, err := lexec.Run(context.Background(), hdiutilPath, args, lexec.ExecOpts{Launch: lexec.HostLaunch("test volume"), Timeout: 2 * time.Minute})
	if err == nil && code != 0 {
		err = fmt.Errorf("exit %d", code)
	}
	return out, err
}
