package metadatacall

import (
	"encoding/binary"
	"slices"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMetadataDescriptorsAndMutationFlags(t *testing.T) {
	for _, test := range []struct {
		nr   uint32
		args [6]uint64
		want Operand
	}{
		{unix.SYS_FCHMOD, [6]uint64{11, 0644}, Operand{Operation: mode, Directory: 11, Empty: true}},
		{unix.SYS_FCHOWNAT, [6]uint64{21, 42, 0, 0, unix.AT_EMPTY_PATH | unix.AT_SYMLINK_NOFOLLOW}, Operand{Operation: owner, Directory: 21, Address: 42, Empty: true, NoFollow: true}},
		{unix.SYS_LREMOVEXATTR, [6]uint64{42}, Operand{Operation: xattr, Directory: unix.AT_FDCWD, Address: 42, NoFollow: true}},
		{unix.SYS_FCHMODAT2, [6]uint64{21, 42, 0644, 1 << 13}, Operand{}},
		{unix.SYS_UTIMENSAT, [6]uint64{21, 0, 0}, Operand{Operation: times, Directory: 21, Empty: true}},
	} {
		got, ok := Decode(test.nr, test.args, nil)
		if !ok || got != test.want || !slices.Contains(Numbers(), test.nr) {
			t.Fatalf("syscall %d: got %+v,%v, want %+v", test.nr, got, ok, test.want)
		}
	}
	if _, ok := Decode(unix.SYS_READ, [6]uint64{}, nil); ok {
		t.Fatal("read classified as metadata")
	}
	for _, pair := range [][2]uint64{{unix.UTIME_OMIT, unix.UTIME_OMIT}, {1_000_000_000, 0}, {0, unix.UTIME_NOW}} {
		o, _ := Decode(unix.SYS_UTIMENSAT, [6]uint64{0, 1, 2}, func(_ uint64, b []byte) error {
			binary.NativeEndian.PutUint64(b[8:], pair[0])
			binary.NativeEndian.PutUint64(b[24:], pair[1])
			return nil
		})
		if (o.Operation != "") != (pair[0] == 0) {
			t.Fatalf("timestamps %v: %+v", pair, o)
		}
	}
}
