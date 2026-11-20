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

func TestWriterRetiresAnInitialLeaseItNeverAttached(t *testing.T) {
	writer := &moduleMountWriter{unattached: map[uint64]string{4: "/tmp/fixture/004"}}
	testutil.FailErr(t, "retire an unattached initial lease",
		writer.accept(mountTransfer{kind: mountTransferRemove, sequence: 1, id: 4, path: "/tmp/fixture/004"}, -1))
	if len(writer.unattached) != 0 {
		t.Fatal("the unattached lease stayed recorded")
	}
	if err := writer.accept(mountTransfer{kind: mountTransferRemove, sequence: 2, id: 4, path: "/tmp/fixture/004"}, -1); err == nil {
		t.Fatal("a second removal of the same lease was accepted")
	}
}

// replyToInitialLease runs the stage's side of one initial transfer against a
// writer that answers with reply and, when held is set, a descriptor.
func replyToInitialLease(t *testing.T, reply func(mountTransfer) mountTransfer, held bool) error {
	t.Helper()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "create private transfer pair", err)
	t.Cleanup(func() { _ = unix.Close(pair[0]); _ = unix.Close(pair[1]) })
	tree, err := os.CreateTemp(t.TempDir(), "tree")
	testutil.FailErr(t, "create prepared tree stand-in", err)
	view := &preparedModuleView{
		plan:  moduleViewPlan{staging: []byte("2 1 8:1 / / rw - ext4 /dev/root rw\n11 2 0:30 / /tmp/fixture/004 ro - fuse.fixture fixture ro\n"), initial: []initialMountLease{{lease: 1, mountID: 11}}},
		trees: []*os.File{tree},
	}
	done := make(chan error, 1)
	go func() { done <- transferInitialMountLeases(pair[0], view) }()
	message, fd, err := receiveMountTransfer(pair[1])
	testutil.FailErr(t, "receive the initial lease", err)
	if fd >= 0 {
		_ = unix.Close(fd)
	}
	extra := -1
	if held {
		extra = int(tree.Fd())
	}
	testutil.FailErr(t, "answer the initial lease", sendMountTransfer(pair[1], reply(message), extra))
	return <-done
}

func TestStageAcceptsAnAttachedOrPrunedInitialLeaseAndNamesEveryOtherReply(t *testing.T) {
	ack := func(m mountTransfer) mountTransfer {
		return mountTransfer{kind: mountTransferAck, sequence: m.sequence}
	}
	denied := func(m mountTransfer) mountTransfer { m.kind = mountTransferDenied; return m }
	testutil.FailErr(t, "attached initial lease", replyToInitialLease(t, ack, false))
	testutil.FailErr(t, "pruned initial lease", replyToInitialLease(t, denied, false))
	if err := replyToInitialLease(t, ack, true); err == nil {
		t.Fatal("a reply carrying a descriptor was accepted")
	}
	if err := replyToInitialLease(t, func(m mountTransfer) mountTransfer {
		return mountTransfer{kind: mountTransferAck, sequence: m.sequence + 1}
	}, false); err == nil {
		t.Fatal("a reply for another sequence was accepted")
	}
}
