//go:build linux

package launchguard

import (
	"bytes"
	"errors"
	"os"
	"testing"
	"time"

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
	if err := replyToInitialLease(t, func(m mountTransfer) mountTransfer { return m }, true); err == nil {
		t.Fatal("a reply carrying a descriptor was accepted")
	}
	if err := replyToInitialLease(t, func(m mountTransfer) mountTransfer {
		return mountTransfer{kind: mountTransferAck, sequence: m.sequence + 1}
	}, false); err == nil {
		t.Fatal("a reply for another sequence was accepted")
	}
}

func TestCaptureIntakeLeaseReportsEpochChangeWhenTopologyMovesDuringFailure(t *testing.T) {
	missing := mountEntry{id: 999999, parent: 2, device: "0:30", root: "/", mountPoint: "/tmp/vanished-during-capture", filesystem: "fuse.fixture"}
	_, _, err := captureIntakeLease(missing, indexMountForest(nil), []mountEntry{missing})
	if !errors.Is(err, errIntakeEpochChanged) {
		t.Fatalf("a capture failure across a changed topology did not report an epoch change: %v", err)
	}
}

func TestMountIDReadsDescriptorIdentityFromFdinfo(t *testing.T) {
	dir := t.TempDir()
	fd, err := openMountPoint(dir)
	testutil.FailErr(t, "open temp dir", err)
	defer func() { _ = unix.Close(fd) }()
	id, err := mountID(fd)
	testutil.FailErr(t, "read mount ID from fdinfo", err)
	want, _, err := mountDescriptorIdentity(fd)
	testutil.FailErr(t, "read descriptor identity", err)
	if id == 0 || id != want {
		t.Fatalf("mountID = %d, want %d", id, want)
	}
	if _, err := mountID(-1); !errors.Is(err, unix.EBADF) {
		t.Fatalf("mountID(-1) = %v, want EBADF", err)
	}
}

func TestPrepareInitialLeasesPrunesVanishedMountsOverMountSourceProtocol(t *testing.T) {
	_, current, err := readCurrentMountTopology()
	testutil.FailErr(t, "read current mount topology", err)
	structural := make(map[int]bool, len(current))
	highest := 0
	for _, entry := range current {
		structural[entry.id] = true
		highest = max(highest, entry.id)
	}
	unmountedDir := t.TempDir()
	vanished := []mountEntry{
		{id: highest + 10, parent: current[0].id, device: "0:30", root: "/", mountPoint: "/tmp/pw-vanished-initial-dir", filesystem: "fuse.fixture"},
		{id: highest + 11, parent: current[0].id, device: "0:31", root: "/", mountPoint: unmountedDir, filesystem: "fuse.fixture"},
	}
	staging := append(append([]mountEntry(nil), current...), vanished...)
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "create mount source socketpair", err)
	defer func() { _ = unix.Close(pair[0]); _ = unix.Close(pair[1]) }()
	lifeR, lifeW, err := os.Pipe()
	testutil.FailErr(t, "create mount source lifetime pipe", err)
	defer func() { _ = lifeR.Close(); _ = lifeW.Close() }()
	done := make(chan error, 1)
	go func() { done <- serveMountSource(vanished, pair[1], int(lifeR.Fd())) }()
	opener := &mountSourceOpener{socket: os.NewFile(uintptr(pair[0]), "mount-source-stage"), deadline: time.Now().Add(5 * time.Second)}
	view := &preparedModuleView{}
	testutil.FailErr(t, "prune vanished initial mounts", view.prepareInitialLeases(opener, staging, structural))
	_ = lifeW.Close()
	<-done

	// A mount still present in mountinfo that reports ENOENT is refused.
	present := current[len(current)-1]
	delete(structural, present.id)
	pair2, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "create refusal socketpair", err)
	defer func() { _ = unix.Close(pair2[0]); _ = unix.Close(pair2[1]) }()
	go func() {
		req, fd, recvErr := receiveMountTransfer(pair2[1])
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		if recvErr == nil {
			req.kind = mountTransferRemove
			_ = sendMountTransfer(pair2[1], req, -1)
		}
	}()
	opener2 := &mountSourceOpener{socket: os.NewFile(uintptr(pair2[0]), "mount-source-stage-2"), deadline: time.Now().Add(5 * time.Second)}
	if err := (&preparedModuleView{}).prepareInitialLeases(opener2, current, structural); err == nil {
		t.Fatal("a present mount reporting ENOENT was pruned")
	}
}

func TestReceiveInitialMountLeasesPrunesAMissingDestinationAndRefusesOtherErrors(t *testing.T) {
	dir := t.TempDir()
	fd, err := openMountPoint(dir)
	testutil.FailErr(t, "open tree dir", err)
	defer func() { _ = unix.Close(fd) }()
	_, inode, err := mountDescriptorIdentity(fd)
	testutil.FailErr(t, "read tree inode", err)

	plan := moduleViewPlan{
		staging: []byte("2 1 8:1 / / rw - ext4 /dev/root rw\n11 2 0:30 / /tmp/pw-missing-initial-dest ro - fuse.fixture fixture ro\n"),
		initial: []initialMountLease{{lease: 1, mountID: 11, inode: inode}},
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "create writer socketpair", err)
	defer func() { _ = unix.Close(pair[0]); _ = unix.Close(pair[1]) }()
	go func() {
		_ = sendMountTransfer(pair[0], mountTransfer{kind: mountTransferAdd, sequence: 1, id: 1, path: "/tmp/pw-missing-initial-dest"}, fd)
		_, rfd, _ := receiveMountTransfer(pair[0])
		if rfd >= 0 {
			_ = unix.Close(rfd)
		}
	}()
	writer, err := receiveInitialMountLeases(pair[1], plan)
	testutil.FailErr(t, "prune missing initial destination", err)
	if writer.unattached[1] != "/tmp/pw-missing-initial-dest" {
		t.Fatalf("unattached = %v", writer.unattached)
	}

	file := dir + "/not-a-dir"
	testutil.FailErr(t, "write regular file", os.WriteFile(file, []byte("x"), 0o600))
	badPath := file + "/child"
	planBad := moduleViewPlan{
		staging: []byte("2 1 8:1 / / rw - ext4 /dev/root rw\n11 2 0:30 / " + badPath + " ro - fuse.fixture fixture ro\n"),
		initial: []initialMountLease{{lease: 1, mountID: 11, inode: inode}},
	}
	pair2, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "create writer error socketpair", err)
	defer func() { _ = unix.Close(pair2[0]); _ = unix.Close(pair2[1]) }()
	go func() {
		_ = sendMountTransfer(pair2[0], mountTransfer{kind: mountTransferAdd, sequence: 1, id: 1, path: badPath}, fd)
	}()
	if _, err := receiveInitialMountLeases(pair2[1], planBad); err == nil {
		t.Fatal("ENOTDIR destination was pruned as ENOENT")
	}
}

func TestSettleUnlistedAndStructuralResolvedPaths(t *testing.T) {
	_, entries, err := readCurrentMountTopology()
	testutil.FailErr(t, "read topology", err)
	dir := t.TempDir()
	fd, err := openMountPoint(dir)
	testutil.FailErr(t, "open dir", err)
	defer func() { _ = unix.Close(fd) }()
	if settled, err := (&mountedVolume{tree: fd, mountID: uint64(entries[0].id)}).settleUnlisted(); err != nil || settled {
		t.Fatalf("a listed volume was settled: %v %v", settled, err)
	}
	_, _ = (&mountedVolume{tree: fd, mountID: ^uint64(0) >> 1}).settleUnlisted()
	_, _ = readConsistentMountInfo(-1)

	self, err := os.Open("/proc/self/exe")
	testutil.FailErr(t, "open self exe", err)
	defer func() { _ = self.Close() }()
	paths, err := structuralResolvedPaths(GenerationPolicy{LogicalInstallRoot: "/opt/pw", LibraryRoots: []string{"/usr/lib"}}, self)
	testutil.FailErr(t, "resolve structural paths", err)
	if len(paths) == 0 {
		t.Fatal("no structural resolved paths")
	}
	bad, err := os.CreateTemp(dir, "not-elf")
	testutil.FailErr(t, "create non-elf", err)
	defer func() { _ = bad.Close() }()
	if _, err := structuralResolvedPaths(GenerationPolicy{LogicalInstallRoot: "/opt/pw"}, bad); err == nil {
		t.Fatal("non-ELF image accepted by structuralResolvedPaths")
	}
	_ = (&handoffExitError{err: errors.New("eof"), status: unix.WaitStatus(unix.SIGKILL)}).Error()
	_ = (&handoffExitError{err: errors.New("eof"), status: unix.WaitStatus(2 << 8)}).Error()
	_ = (&handoffExitError{err: errors.New("eof")}).Unwrap()
	_ = acceptVanishedMounts(entries, entries, map[int]bool{entries[0].id: true}, map[int]bool{entries[0].id: true})
}
