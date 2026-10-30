//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"os"
	"slices"

	"golang.org/x/sys/unix"
)

// preparedModuleView owns only detached initial leases and the original
// receive-only namespace handle. No source mount pins survive preparation.
type preparedModuleView struct {
	source   *os.File
	guardian *os.File
	plan     moduleViewPlan
	trees    []*os.File
}

func (view *preparedModuleView) close() {
	if view.guardian != nil {
		_ = view.guardian.Close()
		view.guardian = nil
	}
	if view.source != nil {
		_ = view.source.Close()
		view.source = nil
	}
	for _, tree := range view.trees {
		_ = tree.Close()
	}
	view.trees = nil
}

func readCurrentMountTopology() ([]byte, []mountEntry, error) {
	file, err := os.Open("/proc/thread-self/mountinfo")
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = file.Close() }()
	raw, err := readMountInfo(int(file.Fd()), nil)
	if err != nil {
		return nil, nil, err
	}
	entries, err := parseMountTopology(raw)
	return raw, entries, err
}

// Freeze the app staging namespace before its final inventory. Intake keeps
// the separate original namespace, so an arrival cannot become a locked
// inherited app mount between pruning and creation of U_view.
func prepareModuleView(spec EntrySpec, frame, origin, entry, desktop *os.File) (_ *preparedModuleView, err error) {
	if err := initialUserNamespace(); err != nil {
		return nil, err
	}
	sourceRaw, sourceEntries, err := readCurrentMountTopology()
	if err != nil {
		return nil, err
	}
	sourceStructural, err := structuralMounts(spec.Generation, desktop, sourceEntries)
	if err != nil {
		return nil, err
	}
	code, err := captureModuleCodeRoots(spec.Generation, desktop)
	if err != nil {
		return nil, err
	}
	defer closeModuleCodeRoots(code)
	_, sourceAfter, err := readCurrentMountTopology()
	if err != nil || !sameMountEpoch(sourceEntries, sourceAfter, nil) {
		return nil, errors.New("source topology changed during structural selection")
	}
	source, err := os.Open("/proc/thread-self/ns/mnt")
	if err != nil {
		return nil, err
	}
	view := &preparedModuleView{source: source}
	defer func() {
		if err != nil {
			view.close()
		}
	}()
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return nil, err
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return nil, err
	}
	stagingRaw, staging, err := readCurrentMountTopology()
	if err != nil {
		return nil, err
	}
	structural, err := structuralMounts(spec.Generation, desktop, staging)
	if err != nil {
		return nil, err
	}
	selected := make([]int, 0, len(sourceStructural))
	for id := range sourceStructural {
		selected = append(selected, id)
	}
	mapped, err := correlateStructuralMounts(sourceEntries, selected, staging)
	if err != nil || !sameStructuralSelection(mapped, structural) {
		return nil, errors.New("selected structural objects changed across staging clone")
	}
	if err := verifyModuleCodeRoots(code); err != nil {
		return nil, err
	}
	view.plan = moduleViewPlan{source: sourceRaw, staging: stagingRaw, structural: selected}
	opener, err := startMountSource(frame, origin, entry, stagingRaw)
	if err != nil {
		return nil, err
	}
	defer opener.close()
	if err := view.prepareInitialLeases(opener, staging, structural); err != nil {
		return nil, err
	}
	view.guardian, err = opener.guardianNamespace(origin)
	if err != nil {
		return nil, err
	}
	if err := opener.finish(); err != nil {
		return nil, err
	}
	if err := projectModuleCodeRoots(code); err != nil {
		return nil, err
	}
	// Preparation prunes children first; attachment restores parents first.
	slices.Reverse(view.trees)
	slices.Reverse(view.plan.initial)
	for index := range view.plan.initial {
		view.plan.initial[index].lease = uint64(index) + 1
	}
	return view, nil
}

func (view *preparedModuleView) prepareInitialLeases(opener *mountSourceOpener, staging []mountEntry, structural map[int]bool) error {
	// A covered mount is pruned after its cover but is never imported as a
	// second same-point stack. A visible nested mount keeps its own lease.
	covered, err := hiddenMountRoots(staging)
	if err != nil {
		return err
	}
	removed := make(map[int]bool)
	for _, mount := range volumeDetachOrder(staging, structural) {
		fd, err := opener.open(mount)
		if errors.Is(err, unix.EACCES) {
			_, current, readErr := readCurrentMountTopology()
			if readErr != nil || !sameMountEpoch(staging, current, removed) {
				return errors.New("initial mount topology changed before administrative pruning")
			}
			err = pruneAdministrativeMount(mount, current)
			if err != nil {
				return fmt.Errorf("prune administrative mount: %w", err)
			}
			removed[mount.id] = true
			_, after, readErr := readCurrentMountTopology()
			if readErr != nil || !sameMountEpoch(staging, after, removed) {
				return errors.New("administrative pruning changed unexpected topology")
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("open initial mount %q (%d) for private view: %w", mount.mountPoint, mount.id, err)
		}
		if err := view.prepareInitialLease(fd, mount, !covered[mount.id]); err != nil {
			_ = unix.Close(fd)
			return err
		}
		// Descriptor-relative unmount is safe only after complete epoch and
		// visible-ID checks, with this stage the sole namespace mount writer.
		_, current, err := readCurrentMountTopology()
		if err != nil || !sameMountEpoch(staging, current, removed) {
			_ = unix.Close(fd)
			return errors.New("initial mount topology changed during preparation")
		}
		if err := verifyMountSourceRoot(fd, mount); err != nil {
			_ = unix.Close(fd)
			return err
		}
		err = unix.Unmount(mountDescriptorPath(fd), unix.MNT_DETACH)
		_ = unix.Close(fd)
		if err != nil {
			return fmt.Errorf("prune inherited volume: %w", err)
		}
		removed[mount.id] = true
	}
	_, after, err := readCurrentMountTopology()
	if err != nil || !sameMountEpoch(staging, after, removed) {
		return errors.New("initial mount topology changed after preparation")
	}
	return nil
}

func (view *preparedModuleView) prepareInitialLease(fd int, mount mountEntry, visible bool) error {
	if !visible {
		return nil
	}
	_, inode, err := mountDescriptorIdentity(fd)
	if err != nil {
		return err
	}
	tree, err := prepareMountAttachment(fd)
	if err != nil {
		return err
	}
	lease := uint64(len(view.plan.initial)) + 1
	view.plan.initial = append(view.plan.initial, initialMountLease{lease: lease, mountID: mount.id, inode: inode})
	view.trees = append(view.trees, os.NewFile(uintptr(tree), "prepared-initial-volume"))
	return nil
}

func sameStructuralSelection(a, b map[int]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if !b[id] {
			return false
		}
	}
	return true
}
