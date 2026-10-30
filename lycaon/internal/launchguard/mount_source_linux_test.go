//go:build linux

package launchguard

import (
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func sourceTestRoot(t *testing.T) mountEntry {
	t.Helper()
	info, err := os.Open("/proc/thread-self/mountinfo")
	testutil.FailErr(t, "open actual topology", err)
	defer func() { _ = info.Close() }()
	raw, err := readMountInfo(int(info.Fd()), nil)
	testutil.FailErr(t, "read actual topology", err)
	entries, err := parseMountTopology(raw)
	testutil.FailErr(t, "decode actual topology", err)
	fd, err := openMountPoint("/")
	testutil.FailErr(t, "open namespace root", err)
	defer func() { _ = unix.Close(fd) }()
	id, _, err := mountDescriptorIdentity(fd)
	testutil.FailErr(t, "read root descriptor kernel facts", err)
	for _, entry := range entries {
		if uint64(entry.id) == id && entry.mountPoint == "/" {
			return entry
		}
	}
	t.Fatal("opened namespace root is absent from complete topology")
	return mountEntry{}
}

func TestMountSourceDescriptorProvesRootNotJustContainingMount(t *testing.T) {
	root := sourceTestRoot(t)
	fd, err := openMountPoint("/")
	testutil.FailErr(t, "open selected source root", err)
	defer func() { _ = unix.Close(fd) }()
	testutil.FailErr(t, "authenticate root descriptor", verifyMountSourceRoot(fd, root))
	wrong := root
	wrong.mountPoint = "/not-the-root"
	if err := verifyMountSourceRoot(fd, wrong); err == nil {
		t.Fatal("accepted a containing mount ID without the exact root alias")
	}
	if _, _, err := mountDescriptorIdentity(-1); err == nil {
		t.Fatal("accepted an invalid source descriptor")
	}
}

func TestMountSourceTransferIsBoundedBySealedSelectionAndSequence(t *testing.T) {
	root := sourceTestRoot(t)
	for _, mode := range []string{"complete", "duplicate", "unselected", "out of order"} {
		t.Run(mode, func(t *testing.T) {
			pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
			testutil.FailErr(t, "create source transport", err)
			defer func() { _ = unix.Close(pair[0]) }()
			lifeR, lifeW, err := os.Pipe()
			testutil.FailErr(t, "create source lifetime", err)
			defer func() { _ = lifeR.Close(); _ = lifeW.Close() }()
			done := make(chan error, 1)
			go func() {
				done <- serveMountSource([]mountEntry{root}, pair[1], int(lifeR.Fd()))
				_ = unix.Close(pair[1])
			}()
			request := mountTransfer{kind: mountTransferOpen, sequence: 1, id: uint64(root.id), path: root.mountPoint}
			switch mode {
			case "unselected":
				request.id++
			case "out of order":
				request.sequence++
			}
			testutil.FailErr(t, "request selected source", sendMountTransfer(pair[0], request, -1))
			if mode == "unselected" || mode == "out of order" {
				if err := <-done; err == nil {
					t.Fatal("accepted unauthorized source request")
				}
				return
			}
			reply, fd, err := receiveMountTransfer(pair[0])
			testutil.FailErr(t, "receive selected source", err)
			defer func() { _ = unix.Close(fd) }()
			request.kind = mountTransferAdd
			if reply != request {
				t.Fatalf("source reply %v differs from %v", reply, request)
			}
			testutil.FailErr(t, "verify transferred exact root", verifyMountSourceRoot(fd, root))
			if mode == "duplicate" {
				request.kind, request.sequence = mountTransferOpen, 2
				testutil.FailErr(t, "repeat selected source", sendMountTransfer(pair[0], request, -1))
				if err := <-done; err == nil {
					t.Fatal("accepted duplicate source pin")
				}
				return
			}
			testutil.FailErr(t, "finish source requests", sendMountTransfer(pair[0], mountTransfer{kind: mountTransferReady, sequence: 2}, -1))
			ack, unexpected, err := receiveMountTransfer(pair[0])
			testutil.FailErr(t, "receive source completion", err)
			if ack.kind != mountTransferAck || ack.sequence != 2 || unexpected >= 0 {
				t.Fatal("invalid source completion")
			}
			testutil.FailErr(t, "source releases every local pin", <-done)
		})
	}
}

func TestMountSourceDeadlineAbortsReceiveAndOnlyItsOwnedChild(t *testing.T) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "create stalled source transport", err)
	defer func() { _ = unix.Close(pair[1]) }()
	parent := os.NewFile(uintptr(pair[0]), "stalled-source")
	defer func() { _ = parent.Close() }()
	child := exec.Command("sleep", "30")
	testutil.FailErr(t, "start owned stalled child", child.Start())
	defer func() { _ = child.Process.Kill() }()
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	opener := &mountSourceOpener{process: child, socket: parent, deadline: time.Now().Add(30 * time.Millisecond), finished: done}
	opener.armDeadline()
	defer opener.stopTimer()
	start := time.Now()
	_, fd, receiveErr := receiveMountTransfer(pair[0])
	if receiveErr == nil || fd >= 0 || time.Since(start) > 2*time.Second {
		t.Fatalf("deadline did not abort stalled transport: fd=%d err=%v elapsed=%s", fd, receiveErr, time.Since(start))
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("deadline left its stalled child running normally")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deadline did not terminate its owned child")
	}
}

func TestMountSourceTopologyBindsTheExactStagingNamespace(t *testing.T) {
	raw := make([]byte, sourceTopologyHeader)
	copy(raw, sourceTopologyMagic[:])
	binary.LittleEndian.PutUint32(raw[8:], 1)
	binary.LittleEndian.PutUint64(raw[16:], 4)
	binary.LittleEndian.PutUint64(raw[24:], 12345)
	raw = append(raw, []byte("2 1 8:1 / / rw - ext4 /dev/root rw\n")...)
	entries, identity, err := decodeSourceTopology(raw)
	testutil.FailErr(t, "decode bound staging topology", err)
	if len(entries) != 1 || identity.device != 4 || identity.inode != 12345 {
		t.Fatal("staging namespace selection changed")
	}
	for _, offset := range []int{0, 8, 12, 16, 24} {
		bad := append([]byte(nil), raw...)
		if offset == 0 || offset == 8 || offset == 12 {
			bad[offset] ^= 3
		} else {
			binary.LittleEndian.PutUint64(bad[offset:], 0)
		}
		if _, _, err := decodeSourceTopology(bad); err == nil {
			t.Fatalf("accepted malformed staging identity at byte %d", offset)
		}
	}
}

func TestMountSourceRefusesInvalidSelectedIdentityAndPollWidth(t *testing.T) {
	for _, id := range []int{-1, 0} {
		selected := mountEntry{id: id, filesystem: "ext4"}
		if err := verifyMountSourceRoot(-1, selected); err == nil {
			t.Fatalf("invalid selected mount identity admitted: %d", id)
		}
		if err := validatePruneParent(-1, selected); err == nil {
			t.Fatalf("invalid prune parent identity admitted: %d", id)
		}
		if err := serveMountSource([]mountEntry{selected}, -1, -1); err == nil {
			t.Fatalf("invalid source selection entered request loop: %d", id)
		}
	}
	for _, pair := range [][2]int{{math.MaxInt32 + 1, 0}, {0, math.MaxInt32 + 1}, {-1, 0}, {0, -1}} {
		if err := serveMountSource(nil, pair[0], pair[1]); err == nil {
			t.Fatalf("unrepresentable source descriptor entered poll: %v", pair)
		}
	}
}
