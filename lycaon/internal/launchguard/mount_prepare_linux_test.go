//go:build linux

package launchguard

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestMountPreparationPeelsHiddenChildrenAndRestoresOnlyVisibleLeases(t *testing.T) {
	fixture := installLaunchFixture(t)
	testutil.FailErr(t, "prepare actual covered initial leases", sudoFixture(t, "mount-prepare", fixture.dir,
		fixtureUIDEnv+"="+strconv.Itoa(os.Getuid()), fixtureGIDEnv+"="+strconv.Itoa(os.Getgid())))
}

func checkInitialMountPreparation(directory, uidText, gidText string) error {
	runtime.LockOSThread()
	uid, uidErr := strconv.ParseUint(uidText, 10, 32)
	gid, gidErr := strconv.ParseUint(gidText, 10, 32)
	if uidErr != nil || gidErr != nil || uid == 0 {
		return errors.New("invalid preparation caller")
	}
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return err
	}
	if err := unix.Mount("", "/", "", unix.MS_PRIVATE|unix.MS_REC, ""); err != nil {
		return err
	}
	root := filepath.Join(directory, "mount-preparation")
	if err := os.Mkdir(root, 0o755); err != nil {
		return err
	}
	defer func() { _ = os.Remove(root) }()
	if err := unix.Mount("tmpfs", root, "tmpfs", 0, "mode=0755,size=1m"); err != nil {
		return err
	}
	defer func() { _ = unix.Unmount(root, unix.MNT_DETACH) }()
	if err := os.Mkdir(filepath.Join(root, "hidden"), 0o755); err != nil {
		return err
	}
	if err := unix.Mount("tmpfs", filepath.Join(root, "hidden"), "tmpfs", 0, "mode=0755,size=1m"); err != nil {
		return err
	}
	if err := unix.Mount("tmpfs", root, "tmpfs", 0, "mode=0755,size=1m"); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(root, "visible"), 0o755); err != nil {
		return err
	}
	if err := unix.Mount("tmpfs", filepath.Join(root, "visible"), "tmpfs", 0, "mode=0755,size=1m"); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "visible", "payload"), []byte("visible lease"), 0o644); err != nil {
		return err
	}
	policyPath, lockDirectory = filepath.Join(directory, "launch.json"), filepath.Join(directory, "locks")
	generation, err := CaptureGeneration()
	if err != nil {
		return err
	}
	defer generation.Close()
	frame, err := sealEntrySpec(EntrySpec{Version: 1, SupervisorPID: os.Getpid(), UID: uint32(uid), GID: uint32(gid), Groups: []uint32{uint32(gid)}, Generation: generation.Policy, Image: generation.DesktopImage})
	if err != nil {
		return err
	}
	defer func() { _ = frame.Close() }()
	origin, err := os.Open("/proc/self/ns/user")
	if err != nil {
		return err
	}
	defer func() { _ = origin.Close() }()
	raw, entries, err := readCurrentMountTopology()
	if err != nil {
		return err
	}
	structural := make(map[int]bool)
	var sourceIDs []int
	for _, entry := range entries {
		if entry.mountPoint != root && !strings.HasPrefix(entry.mountPoint, root+"/") {
			structural[entry.id] = true
			sourceIDs = append(sourceIDs, entry.id)
		}
	}
	opener, err := startMountSource(frame, origin, generation.Entry, raw)
	if err != nil {
		return err
	}
	defer opener.close()
	view := &preparedModuleView{plan: moduleViewPlan{source: raw, staging: raw, structural: sourceIDs}}
	defer view.close()
	if err := view.prepareInitialLeases(opener, entries, structural); err != nil {
		return err
	}
	if err := opener.finish(); err != nil {
		return err
	}
	sealed, err := sealModuleViewPlan(view.plan)
	if err != nil {
		return err
	}
	defer func() { _ = sealed.Close() }()
	decoded, err := readModuleViewPlan(sealed)
	if err != nil || len(decoded.initial) != 2 {
		return errors.New("root-sealed initial lease plan differs")
	}
	if _, err := sealed.WriteAt([]byte{0}, 0); !errors.Is(err, unix.EPERM) {
		return errors.New("module-view plan is writable after publication")
	}
	if err := sealed.Chown(int(uid), -1); err != nil {
		return err
	}
	if _, err := readModuleViewPlan(sealed); err == nil {
		return errors.New("accepted foreign-owned module-view plan")
	}
	if len(view.trees) != 2 {
		return errors.New("preparation imported a hidden underlay or lost a visible lease")
	}
	// The root-stage helper's public ordering is reversed for attachment.
	var mounted []*mountedVolume
	defer func() {
		for index := len(mounted) - 1; index >= 0; index-- {
			_ = mounted[index].detach()
		}
	}()
	for index := len(view.trees) - 1; index >= 0; index-- {
		name := root
		if index == 0 {
			name = filepath.Join(root, "visible")
		}
		fd, err := unix.Dup(int(view.trees[index].Fd()))
		if err != nil {
			return err
		}
		volume, err := attachPreparedMount(uint64(len(mounted)+1), name, fd)
		if err != nil {
			_ = unix.Close(fd)
			return err
		}
		mounted = append(mounted, volume)
	}
	payload, err := os.ReadFile(filepath.Join(root, "visible", "payload"))
	if err != nil || string(payload) != "visible lease" {
		return errors.New("restored visible nested volume is unreadable")
	}
	if _, err := os.Stat(filepath.Join(root, "hidden")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("hidden underlay became visible after preparation")
	}
	if err := checkAttachmentRollbackOwnership(directory); err != nil {
		return err
	}
	return checkAdministrativePruning()
}
