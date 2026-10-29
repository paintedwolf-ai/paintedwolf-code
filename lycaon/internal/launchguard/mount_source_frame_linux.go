//go:build linux

package launchguard

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

const sourceTopologyHeader = 32

var sourceTopologyMagic = [8]byte{'P', 'W', 'M', 'S', 'R', 'C', '0', '1'}

type sourceNamespaceIdentity struct {
	device uint64
	inode  uint64
}

// The initial-root stage authenticates ownership before crossing U_open.
// NS_GET_USERNS in U_open cannot return its ancestor's owning user namespace.
// Root sealing binds the exact staging namespace object to the topology.
func sealSourceTopology(raw []byte) (_ *os.File, err error) {
	if len(raw) > maxMountTopologyBytes {
		return nil, errors.New("source topology exceeds its byte bound")
	}
	if err := initialUserNamespace(); err != nil || os.Geteuid() != 0 {
		return nil, errors.New("source topology requires initial-root stage authority")
	}
	mounts, err := os.Open("/proc/thread-self/ns/mnt")
	if err != nil {
		return nil, err
	}
	defer func() { _ = mounts.Close() }()
	owner, err := unix.IoctlRetInt(int(mounts.Fd()), unix.NS_GET_USERNS)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(owner) }()
	var namespace, parent unix.Stat_t
	if err := unix.Fstat(owner, &parent); err != nil || parent.Ino != 0xEFFFFFFD {
		return nil, errors.New("source staging namespace is not initial-owned")
	}
	if err := unix.Fstat(int(mounts.Fd()), &namespace); err != nil {
		return nil, err
	}
	encoded := make([]byte, sourceTopologyHeader+len(raw))
	copy(encoded, sourceTopologyMagic[:])
	binary.LittleEndian.PutUint32(encoded[8:], 1)
	binary.LittleEndian.PutUint64(encoded[16:], uint64(namespace.Dev))
	binary.LittleEndian.PutUint64(encoded[24:], namespace.Ino)
	copy(encoded[sourceTopologyHeader:], raw)
	if _, _, err := decodeSourceTopology(encoded); err != nil {
		return nil, err
	}
	fd, err := unix.MemfdCreate("painted-wolf-source-topology", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "sealed-source-topology")
	defer func() {
		if err != nil {
			_ = file.Close()
		}
	}()
	if _, err = file.Write(encoded); err != nil {
		return nil, err
	}
	if err := file.Chmod(0o400); err != nil {
		return nil, err
	}
	_, err = unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, entrySeals)
	return file, err
}

func readSourceTopology(file *os.File) ([]mountEntry, sourceNamespaceIdentity, error) {
	if err := rootRegular(file); err != nil {
		return nil, sourceNamespaceIdentity{}, err
	}
	seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals&entrySeals != entrySeals {
		return nil, sourceNamespaceIdentity{}, errors.New("mount source topology is not sealed")
	}
	raw, err := io.ReadAll(io.NewSectionReader(file, 0, maxMountTopologyBytes+sourceTopologyHeader+1))
	if err != nil {
		return nil, sourceNamespaceIdentity{}, err
	}
	return decodeSourceTopology(raw)
}

func decodeSourceTopology(raw []byte) ([]mountEntry, sourceNamespaceIdentity, error) {
	if len(raw) < sourceTopologyHeader || !bytes.Equal(raw[:8], sourceTopologyMagic[:]) || binary.LittleEndian.Uint32(raw[8:]) != 1 || binary.LittleEndian.Uint32(raw[12:]) != 0 {
		return nil, sourceNamespaceIdentity{}, errors.New("invalid source topology frame")
	}
	identity := sourceNamespaceIdentity{device: binary.LittleEndian.Uint64(raw[16:]), inode: binary.LittleEndian.Uint64(raw[24:])}
	if identity.device == 0 || identity.inode == 0 {
		return nil, identity, errors.New("source topology lacks staging namespace identity")
	}
	entries, err := parseMountTopology(raw[sourceTopologyHeader:])
	return entries, identity, err
}
