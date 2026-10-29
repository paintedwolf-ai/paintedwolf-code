//go:build linux

package launchguard

import (
	"errors"

	"golang.org/x/sys/unix"
)

// Group fallback is selected only by SINGLE open_tree's structured EINVAL
// and a complete nested subtree. A later mount_setattr EINVAL never selects it.
func captureIntakeLease(root mountEntry, forest mountForest, current []mountEntry) (lease intakeMountLease, tree int, err error) {
	fd, inode, err := openedIntakeRoot(root)
	if err != nil {
		return lease, -1, err
	}
	defer func() { _ = unix.Close(fd) }()
	lease = intakeMountLease{root: root, members: map[int]mountEntry{root.id: root}, inodes: map[int]uint64{root.id: inode}}
	tree, err = unix.OpenTree(fd, "", unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC|unix.AT_EMPTY_PATH)
	if errors.Is(err, unix.EINVAL) {
		members := forest.subtree(root.id)
		if len(members) <= 1 {
			return lease, -1, err
		}
		lease.members = members
		lease.inodes = make(map[int]uint64, len(members))
		for _, member := range members {
			memberFD, memberInode, openErr := openedIntakeRoot(member)
			if openErr != nil {
				return lease, -1, openErr
			}
			_ = unix.Close(memberFD)
			lease.inodes[member.id] = memberInode
		}
		tree, err = unix.OpenTree(fd, "", unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC|unix.AT_EMPTY_PATH|unix.AT_RECURSIVE)
	}
	if err != nil {
		return lease, -1, err
	}
	prepared := tree
	defer func() {
		if err != nil {
			_ = unix.Close(prepared)
		}
	}()
	if err := lockDetachedMount(tree); err != nil {
		return lease, -1, err
	}
	_, after, err := readCurrentMountTopology()
	if err != nil {
		return lease, -1, err
	}
	if !sameMountEpoch(current, after, nil) {
		return lease, -1, errIntakeEpochChanged
	}
	return lease, tree, nil
}

func (intake *moduleIntake) importArrivals(current []mountEntry, structural map[int]bool) (pending bool, err error) {
	hidden, err := hiddenMountRoots(current)
	if err != nil {
		return false, &intakeReconcileFailure{intakeReconcileHiddenRoots, err}
	}
	owned := make(map[int]bool)
	for _, lease := range intake.leases {
		for id := range lease.members {
			owned[id] = true
		}
	}
	forest := indexMountForest(current)
	order := volumeDetachOrder(current, structural)
	for index := len(order) - 1; index >= 0; index-- {
		entry := order[index]
		if hidden[entry.id] || owned[entry.id] {
			continue
		}
		lease, tree, captureErr := captureIntakeLease(entry, forest, current)
		if errors.Is(captureErr, errIntakeEpochChanged) {
			return true, captureErr
		}
		if captureErr != nil {
			// These are structured source lookup refusals under the caller's
			// credentials. No alternate target or privileged lookup follows.
			if intakeLookupUnavailable(captureErr) {
				pending = true
				// A known atomic group remains indivisible on refusal; never
				// import one of its descendants as a separate fallback lease.
				if len(lease.members) > 1 {
					for id := range lease.members {
						owned[id] = true
					}
				}
				continue
			}
			return false, &intakeReconcileFailure{intakeReconcileCapture, captureErr}
		}
		lease.lease, err = intake.newLease()
		if err != nil {
			_ = unix.Close(tree)
			return false, &intakeReconcileFailure{intakeReconcileLease, err}
		}
		err = intake.exchange(mountTransferAdd, lease.lease, entry.mountPoint, tree)
		_ = unix.Close(tree)
		if errors.Is(err, errIntakeDestinationUnavailable) {
			pending = true
			continue
		}
		if err != nil {
			return false, &intakeReconcileFailure{intakeReconcileArrival, err}
		}
		if intake.leases == nil {
			intake.leases = make(map[uint64]intakeMountLease)
		}
		intake.leases[lease.lease] = lease
		for id := range lease.members {
			owned[id] = true
		}
	}
	return pending, nil
}

func intakeLookupUnavailable(err error) bool {
	var identity *intakeIdentityError
	return !errors.As(err, &identity) && (errors.Is(err, unix.EACCES) || errors.Is(err, unix.ENOENT) ||
		errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ELOOP))
}
