//go:build linux

package launchguard

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// moduleMountWriter is the one mount mutator after U_view creation. It never
// stacks an owned lease: a replacement retires the previous lease first.
// Parent and child leases are distinct; atomic groups have one root lease.
type moduleMountWriter struct {
	volumes map[uint64]*mountedVolume
	roots   map[uint64]*mountedVolume
	// unattached holds initial leases whose destination was gone at startup;
	// the intake retires them like any vanished source.
	unattached map[uint64]string
	sequence   uint64
	lastLease  uint64
}

func (writer *moduleMountWriter) accept(message mountTransfer, tree int) (err error) {
	if writer.sequence == ^uint64(0) || message.sequence != writer.sequence+1 {
		if tree >= 0 {
			_ = unix.Close(tree)
		}
		return errors.New("module mount event sequence exhausted or changed")
	}
	writer.sequence = message.sequence
	switch message.kind {
	case mountTransferAdd:
		if tree < 0 {
			return errors.New("module mount arrival lacks its prepared tree")
		}
		defer func() {
			if err != nil {
				_ = unix.Close(tree)
			}
		}()
		if message.id <= writer.lastLease {
			return errors.New("module mount lease identity was reused")
		}
		if writer.volumes == nil {
			writer.volumes = make(map[uint64]*mountedVolume)
			writer.roots = make(map[uint64]*mountedVolume)
		}
		if len(writer.volumes) >= maxMountTopologyEntries {
			return errors.New("module mount lease bound exceeded")
		}
		writer.lastLease = message.id
		if err := writer.refuseOwnedStack(message.path); err != nil {
			return err
		}
		volume, err := attachPreparedMount(message.id, message.path, tree)
		if err != nil {
			return err
		}
		writer.volumes[message.id] = volume
		writer.roots[volume.mountID] = volume
		writer.lastLease = message.id
		return nil
	case mountTransferRemove:
		if tree >= 0 {
			_ = unix.Close(tree)
			return errors.New("module removal carries a descriptor")
		}
		if path, never := writer.unattached[message.id]; never && path == message.path {
			delete(writer.unattached, message.id)
			return nil
		}
		volume, exists := writer.volumes[message.id]
		if !exists || volume.path != message.path {
			return errors.New("module removal differs from its owned lease")
		}
		if err := volume.detach(); err != nil {
			return fmt.Errorf("retire module mount lease: %w", err)
		}
		delete(writer.roots, volume.mountID)
		delete(writer.volumes, message.id)
		return nil
	default:
		if tree >= 0 {
			_ = unix.Close(tree)
		}
		return errors.New("unexpected module mount writer operation")
	}
}

func (writer *moduleMountWriter) refuseOwnedStack(alias string) error {
	target, err := openMountPoint(alias)
	if err != nil {
		return &mountDestinationError{err: err}
	}
	defer func() { _ = unix.Close(target) }()
	id, inode, err := mountDescriptorIdentity(target)
	if err != nil {
		return err
	}
	if volume := writer.roots[id]; volume != nil {
		_, rootInode, err := mountDescriptorIdentity(volume.tree)
		if err != nil {
			return err
		}
		if rootInode == inode {
			return errors.New("module mount target is an already owned lease root")
		}
	}
	return nil
}

// The stage transfers initial trees in sealed parent-first order. It remains
// responsible for setup lifetime bounds and receives one exact ACK per tree.
func receiveInitialMountLeases(socket int, plan moduleViewPlan) (_ *moduleMountWriter, err error) {
	staging, err := parseMountTopology(plan.staging)
	if err != nil {
		return nil, err
	}
	byID := make(map[int]mountEntry, len(staging))
	for _, entry := range staging {
		byID[entry.id] = entry
	}
	writer := &moduleMountWriter{}
	defer func() {
		if err != nil {
			_ = writer.close()
		}
	}()
	for index, lease := range plan.initial {
		message, tree, err := receiveMountTransfer(socket)
		if err != nil {
			return nil, err
		}
		expected := mountTransfer{kind: mountTransferAdd, sequence: uint64(index) + 1, id: lease.lease, path: byID[lease.mountID].mountPoint}
		if message != expected || tree < 0 {
			if tree >= 0 {
				_ = unix.Close(tree)
			}
			return nil, errors.New("initial mount transfer differs from sealed plan")
		}
		_, inode, identityErr := mountDescriptorIdentity(tree)
		if identityErr != nil || inode != lease.inode {
			_ = unix.Close(tree)
			return nil, errors.New("initial mount root differs from sealed selection")
		}
		reply := mountTransfer{kind: mountTransferAck, sequence: message.sequence}
		if err := writer.accept(message, tree); err != nil {
			// A destination removed after preparation is pruned, as after startup.
			var unavailable *mountDestinationError
			if !errors.As(err, &unavailable) || !errors.Is(err, unix.ENOENT) {
				return nil, err
			}
			if writer.unattached == nil {
				writer.unattached = make(map[uint64]string)
			}
			writer.unattached[message.id] = message.path
			reply = message
			reply.kind = mountTransferDenied
		}
		if err := sendMountTransfer(socket, reply, -1); err != nil {
			return nil, err
		}
	}
	return writer, nil
}

func (writer *moduleMountWriter) close() error {
	// A namespace-local topology gives child/group roots their exact order,
	// including aliases renamed after attachment. Never order by lease age.
	_, entries, err := readCurrentMountTopology()
	if err != nil {
		return errors.Join(err, writer.closeDescriptors())
	}
	byMount := make(map[uint64]*mountedVolume, len(writer.volumes))
	for _, volume := range writer.volumes {
		byMount[volume.mountID] = volume
	}
	var failures []error
	for _, entry := range volumeDetachOrder(entries, nil) {
		if entry.id <= 0 {
			failures = append(failures, errors.New("invalid mounted volume identity"))
			continue
		}
		if volume := byMount[uint64(entry.id)]; volume != nil {
			if err := volume.detach(); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(append(failures, writer.closeDescriptors())...)
}

func (writer *moduleMountWriter) closeDescriptors() error {
	var failures []error
	for lease, volume := range writer.volumes {
		if volume.tree >= 0 {
			failures = append(failures, unix.Close(volume.tree))
			volume.tree = -1
		}
		delete(writer.roots, volume.mountID)
		delete(writer.volumes, lease)
	}
	return errors.Join(failures...)
}

// Keep os.File ownership at the stage's transfer seam. The receiver takes a
// new descriptor; the stage releases its detached-tree reference after ACK.
func transferInitialMountLeases(socket int, view *preparedModuleView) error {
	staging, err := parseMountTopology(view.plan.staging)
	if err != nil {
		return err
	}
	byID := make(map[int]mountEntry, len(staging))
	for _, entry := range staging {
		byID[entry.id] = entry
	}
	if len(view.trees) != len(view.plan.initial) {
		return errors.New("initial lease descriptors differ from plan")
	}
	for index, lease := range view.plan.initial {
		message := mountTransfer{kind: mountTransferAdd, sequence: uint64(index) + 1, id: lease.lease, path: byID[lease.mountID].mountPoint}
		if err := sendMountTransfer(socket, message, int(view.trees[index].Fd())); err != nil {
			return err
		}
		reply, fd, err := receiveMountTransfer(socket)
		if err != nil {
			return fmt.Errorf("receive initial lease attachment reply: %w", err)
		}
		if fd >= 0 {
			_ = unix.Close(fd)
			return errors.New("initial lease attachment reply carried a descriptor")
		}
		denied := message
		denied.kind = mountTransferDenied
		if reply != (mountTransfer{kind: mountTransferAck, sequence: message.sequence}) && reply != denied {
			return errors.New("initial lease attachment reply differs from its lease")
		}
		_ = view.trees[index].Close()
	}
	view.trees = nil
	return nil
}
