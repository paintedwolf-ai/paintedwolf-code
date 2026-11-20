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
	// os.Open would register mountinfo with Go netpoll, whose epoll callback
	// can consume the epoch event the consistent read depends on.
	fd, err := unix.Open("/proc/thread-self/mountinfo", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = unix.Close(fd) }()
	raw, err := readConsistentMountInfo(fd)
	if err != nil {
		return nil, nil, err
	}
	entries, err := parseMountTopology(raw)
	return raw, entries, err
}

// mountInfoReadSize holds a typical table in one buffer; reads still loop.
const mountInfoReadSize = 256 << 10

// readConsistentMountInfo reads mountinfo as one snapshot. The kernel holds the
// namespace lock only for each read(), so a mount or unmount anywhere in the
// namespace, including one propagated from the host, can tear a table read
// across several calls. The poll epoch, cleared before the read and checked
// after it, names such a read, which is then read again.
func readConsistentMountInfo(fd int) ([]byte, error) {
	buf := make([]byte, 0, mountInfoReadSize)
	for {
		if _, err := mountInfoEpochChanged(fd); err != nil {
			return nil, err
		}
		raw, err := readMountInfo(fd, buf)
		if err != nil {
			return nil, err
		}
		changed, err := mountInfoEpochChanged(fd)
		if err != nil {
			return nil, err
		}
		if !changed {
			return raw, nil
		}
		buf = raw[:0]
	}
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
	resolved, err := structuralResolvedPaths(spec.Generation, desktop)
	if err != nil {
		return nil, err
	}
	code, err := captureModuleCodeRoots(spec.Generation, desktop)
	if err != nil {
		return nil, err
	}
	defer closeModuleCodeRoots(code)
	_, sourceAfter, err := readCurrentMountTopology()
	if err != nil {
		return nil, fmt.Errorf("reread source topology after structural selection: %w", err)
	}
	if !sameSelectionTopology(sourceEntries, sourceAfter, sourceStructural, resolved) {
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
	stable := func(stage string) ([]mountEntry, error) {
		_, current, err := readCurrentMountTopology()
		if err != nil {
			return nil, fmt.Errorf("reread initial mount topology %s: %w", stage, err)
		}
		if !acceptVanishedMounts(staging, current, removed, structural) {
			return nil, fmt.Errorf("initial mount topology changed %s", stage)
		}
		return current, nil
	}
	for _, mount := range volumeDetachOrder(staging, structural) {
		fd, err := opener.open(mount)
		if errors.Is(err, unix.ENOENT) {
			// Only a mount the kernel has already detached may be missing.
			if _, err := stable("after a missing initial mount"); err != nil {
				return err
			}
			if !removed[mount.id] {
				return fmt.Errorf("open initial mount %q (%d) for private view: %w", mount.mountPoint, mount.id, unix.ENOENT)
			}
			continue
		}
		if errors.Is(err, unix.EACCES) {
			current, err := stable("before administrative pruning")
			if err != nil {
				return err
			}
			if removed[mount.id] {
				continue
			}
			if err := pruneAdministrativeMount(mount, current); err != nil {
				return fmt.Errorf("prune administrative mount: %w", err)
			}
			removed[mount.id] = true
			if _, err := stable("by administrative pruning"); err != nil {
				return err
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
		if _, err := stable("during preparation"); err != nil {
			_ = unix.Close(fd)
			return err
		}
		if removed[mount.id] {
			// Detached while prepared; the writer prunes its lease at attachment.
			_ = unix.Close(fd)
			continue
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
	_, err = stable("after preparation")
	return err
}

// acceptVanishedMounts adds to removed each non-structural staging mount that
// is no longer present. Removing a directory detaches the mounts on it in every
// namespace that shares it, this private staging copy included. A vanished
// structural mount, or any other difference, still fails.
func acceptVanishedMounts(staging, current []mountEntry, removed, structural map[int]bool) bool {
	present := make(map[int]bool, len(current))
	for _, entry := range current {
		present[entry.id] = true
	}
	merged := make(map[int]bool, len(removed))
	for id := range removed {
		merged[id] = true
	}
	for _, entry := range staging {
		if merged[entry.id] || present[entry.id] {
			continue
		}
		if structural[entry.id] {
			return false
		}
		merged[entry.id] = true
	}
	if !sameMountEpoch(staging, current, merged) {
		return false
	}
	for id := range merged {
		removed[id] = true
	}
	return true
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
