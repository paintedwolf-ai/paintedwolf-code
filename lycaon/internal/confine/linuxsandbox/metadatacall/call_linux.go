// Package metadatacall locates metadata operands for read-only mount observations.
package metadatacall

import (
	"encoding/binary"
	"slices"

	"golang.org/x/sys/unix"
)

const (
	mode  = "file-write-mode"
	owner = "file-write-owner"
	times = "file-write-times"
	xattr = "file-write-xattr"
)

type layout uint8

const (
	path layout = iota
	link
	fd
	at
	atMode
	atOwner
	atTimes
)

type entry struct {
	operation string
	layout    layout
}

var calls = map[uint32]entry{
	unix.SYS_FCHMOD:       {mode, fd},
	unix.SYS_FCHMODAT:     {mode, at},
	unix.SYS_FCHMODAT2:    {mode, atMode},
	unix.SYS_FCHOWN:       {owner, fd},
	unix.SYS_FCHOWNAT:     {owner, atOwner},
	unix.SYS_UTIMENSAT:    {times, atTimes},
	unix.SYS_SETXATTR:     {xattr, path},
	unix.SYS_LSETXATTR:    {xattr, link},
	unix.SYS_FSETXATTR:    {xattr, fd},
	unix.SYS_REMOVEXATTR:  {xattr, path},
	unix.SYS_LREMOVEXATTR: {xattr, link},
	unix.SYS_FREMOVEXATTR: {xattr, fd},
}

func init() { addLegacy(calls) }

// Observes reports whether the syscall has a metadata operand.
func Observes(nr uint32) bool { _, ok := calls[nr]; return ok }

// Numbers lists the syscall numbers observed on the native architecture.
func Numbers() []uint32 {
	list := make([]uint32, 0, len(calls))
	for nr := range calls {
		list = append(list, nr)
	}
	slices.Sort(list)
	return list
}

type Operand struct {
	Operation string
	Directory int32
	Address   uint64
	Empty     bool
	NoFollow  bool
}

// Decode selects the pathname or descriptor used by one metadata syscall.
func Decode(nr uint32, args [6]uint64, read func(uint64, []byte) error) (Operand, bool) {
	e, ok := calls[nr]
	if !ok {
		return Operand{}, false
	}
	o := Operand{Operation: e.operation, Directory: int32(args[0]), Address: args[1]}
	var flags uint32
	switch e.layout {
	case path, link:
		o.Directory, o.Address, o.NoFollow = unix.AT_FDCWD, args[0], e.layout == link
	case fd:
		if o.Directory < 0 {
			return Operand{}, true
		}
		o.Address, o.Empty = 0, true
	case at:
	case atMode:
		flags = uint32(args[3])
	case atOwner:
		flags = uint32(args[4])
	case atTimes:
		flags = uint32(args[3])
		if !setsTime(args[2], read) {
			return Operand{}, true
		}
		o.Empty = o.Address == 0
	}
	if flags & ^uint32(unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW) != 0 {
		return Operand{}, true
	}
	o.NoFollow = o.NoFollow || flags&unix.AT_SYMLINK_NOFOLLOW != 0
	o.Empty = o.Empty || flags&unix.AT_EMPTY_PATH != 0
	return o, true
}

// Two UTIME_OMIT values leave the inode untouched.
func setsTime(address uint64, read func(uint64, []byte) error) bool {
	if address == 0 {
		return true
	}
	var values [32]byte
	if read(address, values[:]) != nil {
		return false
	}
	a, b := binary.NativeEndian.Uint64(values[8:16]), binary.NativeEndian.Uint64(values[24:])
	valid := func(n uint64) bool { return n < 1_000_000_000 || n == unix.UTIME_NOW || n == unix.UTIME_OMIT }
	return valid(a) && valid(b) && (a != unix.UTIME_OMIT || b != unix.UTIME_OMIT)
}
