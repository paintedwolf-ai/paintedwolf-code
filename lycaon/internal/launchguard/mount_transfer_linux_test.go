//go:build linux

package launchguard

import (
	"bytes"
	"os"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMountTransferPreservesOpaqueNamesAndDescriptorCustody(t *testing.T) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "create private transfer pair", err)
	defer func() { _ = unix.Close(pair[0]); _ = unix.Close(pair[1]) }()
	file, err := os.CreateTemp(t.TempDir(), "tree")
	testutil.FailErr(t, "create transfer object", err)
	defer func() { _ = file.Close() }()
	content := []byte("retained tree identity")
	_, err = file.Write(content)
	testutil.FailErr(t, "write transfer object", err)
	want := mountTransfer{kind: mountTransferAdd, sequence: 1, id: 72, path: "/media/space\tnewline\nback\\raw\xff"}
	testutil.FailErr(t, "send exact name", sendMountTransfer(pair[0], want, int(file.Fd())))
	got, fd, err := receiveMountTransfer(pair[1])
	testutil.FailErr(t, "receive exact name", err)
	defer func() { _ = unix.Close(fd) }()
	if got != want {
		t.Fatalf("transfer = %+v, want %+v", got, want)
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	testutil.FailErr(t, "inspect received descriptor flags", err)
	if flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("received descriptor was inherited across exec")
	}
	read := make([]byte, len(content))
	_, err = unix.Pread(fd, read, 0)
	testutil.FailErr(t, "read transferred object", err)
	if !bytes.Equal(read, content) {
		t.Fatalf("received bytes %q", read)
	}
	for _, invalid := range []mountTransfer{
		{kind: mountTransferAdd, id: 1, path: "relative"},
		{kind: mountTransferAdd, id: 1, path: "/a/../b"},
		{kind: mountTransferAdd, id: 0, path: "/a"},
		{kind: mountTransferRemove, id: 1, path: "/a\x00b"},
		{kind: mountTransferReady, id: 1}, {kind: 99},
	} {
		if invalid.validate() == nil {
			t.Fatalf("accepted invalid transfer %+v", invalid)
		}
	}
	if sendMountTransfer(pair[0], want, -1) == nil || sendMountTransfer(pair[0], mountTransfer{kind: mountTransferAck, sequence: 1}, int(file.Fd())) == nil {
		t.Fatal("accepted incorrect descriptor cardinality")
	}
}

func TestMountTransferDenialHasExactIdentityAndNoDescriptor(t *testing.T) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "create denial transport", err)
	defer func() { _ = unix.Close(pair[0]); _ = unix.Close(pair[1]) }()
	want := mountTransfer{kind: mountTransferDenied, sequence: 9, id: 17, path: "/admin/root"}
	testutil.FailErr(t, "send typed denial", sendMountTransfer(pair[0], want, -1))
	got, fd, err := receiveMountTransfer(pair[1])
	testutil.FailErr(t, "receive typed denial", err)
	if got != want || fd >= 0 {
		t.Fatal("denial changed selection or carried a descriptor")
	}
	if sendMountTransfer(pair[0], want, pair[1]) == nil {
		t.Fatal("denial accepted descriptor authority")
	}
}
