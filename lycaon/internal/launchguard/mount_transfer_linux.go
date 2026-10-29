//go:build linux

package launchguard

import (
	"encoding/binary"
	"errors"
	"io"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// This private, launch-local protocol is carried only by a socketpair retained
// by the watcher and its intake. Paths are opaque bytes, including mountinfo's
// decoded whitespace; JSON would change invalid UTF-8 filenames.
const (
	mountTransferHeader = 24
	mountTransferPath   = 4095
	mountTransferAdd    = 1
	mountTransferRemove = 2
	mountTransferAdopt  = 3
	mountTransferReady  = 4
	mountTransferAck    = 5
	mountTransferOpen   = 6
	mountTransferDenied = 7
	mountTransferPlan   = 8
)

type mountTransfer struct {
	kind     byte
	sequence uint64
	id       uint64
	path     string
}

func (message mountTransfer) validate() error {
	if message.sequence == 0 {
		return errors.New("invalid mount transfer sequence")
	}
	switch message.kind {
	case mountTransferAdd, mountTransferRemove, mountTransferAdopt, mountTransferOpen, mountTransferDenied:
		if message.id == 0 || len(message.path) > mountTransferPath || !filepath.IsAbs(message.path) || filepath.Clean(message.path) != message.path || strings.ContainsRune(message.path, 0) {
			return errors.New("invalid mount transfer identity or path")
		}
	case mountTransferReady, mountTransferAck, mountTransferPlan:
		if message.id != 0 || message.path != "" {
			return errors.New("invalid mount transfer control")
		}
	default:
		return errors.New("unknown mount transfer operation")
	}
	return nil
}

func sendMountTransfer(socket int, message mountTransfer, tree int) error {
	if err := message.validate(); err != nil {
		return err
	}
	needsFD := message.kind == mountTransferAdd || message.kind == mountTransferAdopt || message.kind == mountTransferPlan
	if needsFD != (tree >= 0) {
		return errors.New("mount transfer descriptor count differs")
	}
	packet := make([]byte, mountTransferHeader+len(message.path))
	packet[0], packet[1] = 1, message.kind
	binary.LittleEndian.PutUint64(packet[4:], message.sequence)
	binary.LittleEndian.PutUint64(packet[12:], message.id)
	binary.LittleEndian.PutUint32(packet[20:], uint32(len(message.path)))
	copy(packet[mountTransferHeader:], message.path)
	var control []byte
	if tree >= 0 {
		control = unix.UnixRights(tree)
	}
	for {
		n, err := unix.SendmsgN(socket, packet, control, nil, unix.MSG_NOSIGNAL)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err == nil && n != len(packet) {
			return io.ErrShortWrite
		}
		return err
	}
}

// Every received descriptor is closed on all refusal paths, even when another
// ancillary message or an oversized packet makes the operation invalid.
func receiveMountTransfer(socket int) (message mountTransfer, tree int, err error) {
	packet := make([]byte, mountTransferHeader+mountTransferPath)
	control := make([]byte, unix.CmsgSpace(4*4))
	var n, oob, flags int
	for {
		n, oob, flags, _, err = unix.Recvmsg(socket, packet, control, unix.MSG_CMSG_CLOEXEC)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	tree = -1
	if err != nil {
		return message, tree, err
	}
	controls, parseErr := unix.ParseSocketControlMessage(control[:oob])
	var descriptors []int
	invalid := parseErr != nil
	for _, item := range controls {
		if item.Header.Level != unix.SOL_SOCKET || item.Header.Type != unix.SCM_RIGHTS {
			invalid = true
			continue
		}
		fds, rightsErr := unix.ParseUnixRights(&item)
		invalid = invalid || rightsErr != nil
		descriptors = append(descriptors, fds...)
	}
	defer func() {
		if err != nil {
			for _, fd := range descriptors {
				_ = unix.Close(fd)
			}
		}
	}()
	if n == 0 {
		return message, -1, io.EOF
	}
	if invalid || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || n < mountTransferHeader || packet[0] != 1 || packet[2] != 0 || packet[3] != 0 || binary.LittleEndian.Uint32(packet[20:]) != uint32(n-mountTransferHeader) {
		return message, -1, errors.New("invalid mount transfer frame")
	}
	message = mountTransfer{kind: packet[1], sequence: binary.LittleEndian.Uint64(packet[4:]), id: binary.LittleEndian.Uint64(packet[12:]), path: string(packet[mountTransferHeader:n])}
	if err := message.validate(); err != nil {
		return message, -1, err
	}
	wantFD := 0
	if message.kind == mountTransferAdd || message.kind == mountTransferAdopt || message.kind == mountTransferPlan {
		wantFD = 1
	}
	if len(descriptors) != wantFD {
		return message, -1, errors.New("invalid mount transfer descriptor count")
	}
	if wantFD == 1 {
		tree = descriptors[0]
	}
	return message, tree, nil
}
