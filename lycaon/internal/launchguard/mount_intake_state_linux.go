//go:build linux

package launchguard

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

var errIntakeEpochChanged = errors.New("intake source epoch changed")
var errIntakeDestinationUnavailable = errors.New("intake destination is unavailable")

type intakeMountLease struct {
	lease   uint64
	root    mountEntry
	members map[int]mountEntry
	inodes  map[int]uint64
}

type moduleIntake struct {
	socket    int
	sequence  uint64
	lastLease uint64
	leases    map[uint64]intakeMountLease
}

func (intake *moduleIntake) exchange(kind byte, id uint64, path string, tree int) error {
	if intake.sequence == ^uint64(0) {
		return errors.New("module intake sequence exhausted")
	}
	intake.sequence++
	message := mountTransfer{kind: kind, sequence: intake.sequence, id: id, path: path}
	if err := sendMountTransfer(intake.socket, message, tree); err != nil {
		return err
	}
	reply, fd, err := receiveMountTransfer(intake.socket)
	if fd >= 0 {
		_ = unix.Close(fd)
	}
	denial := message
	denial.kind = mountTransferDenied
	if err == nil && fd < 0 && reply == denial && kind == mountTransferAdd {
		return errIntakeDestinationUnavailable
	}
	if err != nil || fd >= 0 || reply != (mountTransfer{kind: mountTransferAck, sequence: message.sequence}) {
		return errors.New("module writer did not acknowledge its exact intake event")
	}
	return nil
}

func (intake *moduleIntake) newLease() (uint64, error) {
	if intake.lastLease == ^uint64(0) {
		return 0, errors.New("module mount lease identities exhausted")
	}
	intake.lastLease++
	return intake.lastLease, nil
}

func openedIntakeRoot(entry mountEntry) (fd int, inode uint64, err error) {
	descriptor, openErr := openMountPoint(entry.mountPoint)
	fd, err = descriptor, openErr
	if err != nil {
		return -1, 0, err
	}
	defer func() {
		if err != nil {
			_ = unix.Close(descriptor)
		}
	}()
	inode, err = verifyIntakeRoot(fd, entry)
	if err != nil {
		return -1, 0, err
	}
	return fd, inode, nil
}

type intakeIdentityError struct{ error }

func (err *intakeIdentityError) Unwrap() error { return err.error }

// Bind a held descriptor to the observed mount and its exact selected root.
func verifyIntakeRoot(fd int, entry mountEntry) (uint64, error) {
	id, inode, err := mountDescriptorIdentity(fd)
	if err != nil {
		return 0, &intakeIdentityError{err}
	}
	if entry.id <= 0 {
		return 0, errors.New("invalid intake mount identity")
	}
	if id != uint64(entry.id) {
		return 0, errIntakeEpochChanged
	}
	alias, err := os.Readlink(mountDescriptorPath(fd))
	if err != nil || alias != entry.mountPoint {
		return 0, errors.New("intake source is not the selected mount root")
	}
	return inode, nil
}

// A held private clone pins its filesystem/root object. Adoption requires the
// complete source facts plus that exact root inode, not a reused mount ID.
func (intake *moduleIntake) adoptInitial(plan moduleViewPlan, current []mountEntry) error {
	staging, err := parseMountTopology(plan.staging)
	if err != nil {
		return err
	}
	byStaging := make(map[int]mountEntry, len(staging))
	for _, entry := range staging {
		byStaging[entry.id] = entry
	}
	byKey := make(map[mountSourceKey][]mountEntry, len(current))
	for _, entry := range current {
		byKey[entry.sourceKey()] = append(byKey[entry.sourceKey()], entry)
	}
	adopted := make(map[uint64]intakeMountLease)
	var stale []initialMountLease
	for _, lease := range plan.initial {
		expected := byStaging[lease.mountID]
		candidates := byKey[expected.sourceKey()]
		matched := false
		if len(candidates) == 1 {
			candidate := candidates[0]
			fd, inode, openErr := openedIntakeRoot(candidate)
			if openErr == nil {
				_ = unix.Close(fd)
				if inode == lease.inode {
					adopted[lease.lease] = intakeMountLease{lease: lease.lease, root: candidate, members: map[int]mountEntry{candidate.id: candidate}, inodes: map[int]uint64{candidate.id: inode}}
					matched = true
				}
			}
		}
		if !matched {
			stale = append(stale, lease)
		}
		if lease.lease > intake.lastLease {
			intake.lastLease = lease.lease
		}
	}
	_, after, err := readCurrentMountTopology()
	if err != nil || !sameMountEpoch(current, after, nil) {
		return errIntakeEpochChanged
	}
	// Remove children first when more than one sealed initial source vanished.
	for index := len(stale) - 1; index >= 0; index-- {
		lease := stale[index]
		if err := intake.exchange(mountTransferRemove, lease.lease, byStaging[lease.mountID].mountPoint, -1); err != nil {
			return err
		}
	}
	intake.leases = adopted
	return nil
}

func (intake *moduleIntake) retireChanged(current []mountEntry) error {
	forest := indexMountForest(current)
	byID := forest.byID
	var changed []intakeMountLease
	for _, lease := range intake.leases {
		valid := true
		for id, expected := range lease.members {
			actual, exists := byID[id]
			if !exists || actual != expected {
				valid = false
				break
			}
			fd, inode, err := openedIntakeRoot(actual)
			if err != nil {
				valid = false
				break
			}
			_ = unix.Close(fd)
			if inode != lease.inodes[id] {
				valid = false
				break
			}
		}
		if valid {
			// A new nested member is a change to an atomic group, even when
			// every old row and root object survived unchanged.
			members := forest.subtree(lease.root.id)
			if len(lease.members) > 1 && len(members) != len(lease.members) {
				valid = false
			}
		}
		if !valid {
			changed = append(changed, lease)
		}
	}
	_, after, err := readCurrentMountTopology()
	if err != nil || !sameMountEpoch(current, after, nil) {
		return errIntakeEpochChanged
	}
	// Actual namespace topology, not lease age, orders nested removals.
	roots := make(map[int]intakeMountLease, len(changed))
	var old []mountEntry
	for _, lease := range intake.leases {
		for _, entry := range lease.members {
			old = append(old, entry)
		}
	}
	for _, lease := range changed {
		roots[lease.root.id] = lease
	}
	// Missing structural parents are represented only as external roots here;
	// the ordering function receives this already cycle-free observed subset.
	for _, entry := range volumeDetachOrder(old, nil) {
		lease, exists := roots[entry.id]
		if !exists {
			continue
		}
		if err := intake.exchange(mountTransferRemove, lease.lease, lease.root.mountPoint, -1); err != nil {
			return err
		}
		delete(intake.leases, lease.lease)
	}
	return nil
}

type mountForest struct {
	byID     map[int]mountEntry
	children map[int][]mountEntry
}

func indexMountForest(entries []mountEntry) mountForest {
	forest := mountForest{byID: make(map[int]mountEntry, len(entries)), children: make(map[int][]mountEntry, len(entries))}
	for _, entry := range entries {
		forest.children[entry.parent] = append(forest.children[entry.parent], entry)
		forest.byID[entry.id] = entry
	}
	return forest
}

func (forest mountForest) subtree(root int) map[int]mountEntry {
	members := make(map[int]mountEntry)
	stack := []int{root}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if entry, exists := forest.byID[id]; exists {
			members[id] = entry
			for _, child := range forest.children[id] {
				stack = append(stack, child.id)
			}
		}
	}
	return members
}
