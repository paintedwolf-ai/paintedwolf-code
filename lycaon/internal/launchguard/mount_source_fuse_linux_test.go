//go:build linux

package launchguard

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestMountSourceOpensCallerFUSEWithoutAllowOther(t *testing.T) {
	fixture := installLaunchFixture(t)
	volume := t.TempDir()
	startSourceFUSE(t, volume, nil)
	got, err := os.ReadFile(filepath.Join(volume, "payload"))
	testutil.FailErr(t, "read ordinary caller FUSE positive control", err)
	if string(got) != "caller-owned-volume\n" {
		t.Fatalf("FUSE positive control read %q", got)
	}
	testutil.FailErr(t, "open actual no-allow-other FUSE through isolated caller role", sudoFixture(t, "mount-source", fixture.dir,
		fixtureUIDEnv+"="+strconv.Itoa(os.Getuid()), fixtureGIDEnv+"="+strconv.Itoa(os.Getgid()), fixtureSourcePathEnv+"="+volume))
}

// A small read-only filesystem speaks the installed Linux fuse.h v7.31 ABI.
// It is an actual unprivileged fusermount mount, without allow_other; no
// external FUSE library, user daemon or desktop service is reused.
func startSourceFUSE(t *testing.T, volume string, module []byte) func() {
	t.Helper()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "create FUSE descriptor transport", err)
	parent, child := os.NewFile(uintptr(pair[0]), "fuse-parent"), os.NewFile(uintptr(pair[1]), "fuse-child")
	defer func() { _ = parent.Close(); _ = child.Close() }()
	command := exec.Command("fusermount3", "-o", "ro,nosuid,nodev,fsname=pw-mount-fixture,subtype=pw_mount_fixture", "--", volume)
	command.Env = append(os.Environ(), "_FUSE_COMMFD=3")
	command.ExtraFiles = []*os.File{child}
	var output strings.Builder
	command.Stdout, command.Stderr = &output, &output
	testutil.FailErr(t, "start own FUSE mount", command.Start())
	mountWaited := false
	defer func() {
		if !mountWaited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	var server *exec.Cmd
	var done chan error
	var stopped sync.Once
	stop := func() {
		stopped.Do(func() {
			if out, err := exec.Command("fusermount3", "-u", "-z", "--", volume).CombinedOutput(); err != nil {
				t.Errorf("remove own FUSE mount: %v %s", err, out)
			}
			if done == nil {
				return
			}
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("FUSE fixture protocol: %v", err)
				}
			case <-time.After(2 * time.Second):
				_ = server.Process.Kill()
				<-done
				t.Error("own FUSE server did not settle after unmount")
			}
		})
	}
	t.Cleanup(stop)
	_ = child.Close()
	data, control := make([]byte, 1), make([]byte, unix.CmsgSpace(4))
	_, n, flags, _, receiveErr := unix.Recvmsg(pair[0], data, control, unix.MSG_CMSG_CLOEXEC)
	var fd = -1
	messages, parseErr := unix.ParseSocketControlMessage(control[:n])
	var received []int
	for _, message := range messages {
		if message.Header.Level == unix.SOL_SOCKET && message.Header.Type == unix.SCM_RIGHTS {
			rights, err := unix.ParseUnixRights(&message)
			if err == nil {
				received = append(received, rights...)
			} else {
				parseErr = err
			}
		}
	}
	if receiveErr == nil && parseErr == nil && flags&(unix.MSG_CTRUNC|unix.MSG_TRUNC) == 0 && len(messages) == 1 && len(received) == 1 {
		fd = received[0]
	}
	for _, right := range received {
		if right != fd {
			_ = unix.Close(right)
		}
	}
	if fd < 0 {
		_ = command.Wait()
		mountWaited = true
		t.Fatalf("FUSE mount did not return a device: %v %s", receiveErr, output.String())
	}
	device := os.NewFile(uintptr(fd), "owned-fuse-device")
	defer device.Close()
	server = exec.Command(os.Args[0], "-test.run=^TestSourceFUSEServerProcess$")
	server.Env = append(os.Environ(), "PW_FUSE_FIXTURE_SERVER=1")
	server.ExtraFiles = []*os.File{device}
	if len(module) > 0 {
		name := filepath.Join(t.TempDir(), "module-input")
		testutil.FailErr(t, "retain own module fixture bytes", os.WriteFile(name, module, 0600))
		file, err := os.Open(name)
		testutil.FailErr(t, "open retained module bytes", err)
		defer file.Close()
		server.ExtraFiles = append(server.ExtraFiles, file)
		server.Env = append(server.Env, "PW_FUSE_FIXTURE_MODULE=1")
	}
	server.Stderr = os.Stderr
	testutil.FailErr(t, "start separate owned FUSE daemon", server.Start())
	_ = device.Close()
	done = make(chan error, 1)
	go func() { done <- server.Wait() }()

	mountErr := command.Wait()
	mountWaited = true
	if err := mountErr; err != nil {
		t.Fatalf("FUSE mount failed: %v %s", err, output.String())
	}
	info, err := os.Open("/proc/thread-self/mountinfo")
	testutil.FailErr(t, "open FUSE mount facts", err)
	defer func() { _ = info.Close() }()
	raw, err := readMountInfo(int(info.Fd()), nil)
	testutil.FailErr(t, "read FUSE mount facts", err)
	entries, err := parseMountTopology(raw)
	testutil.FailErr(t, "decode FUSE mount facts", err)
	for _, entry := range entries {
		if entry.mountPoint == volume {
			if entry.filesystem != "fuse.pw_mount_fixture" || hasMountOption(entry.superOptions, "allow_other") || !hasMountOption(entry.superOptions, "user_id="+strconv.Itoa(os.Getuid())) {
				t.Fatalf("FUSE ownership/access facts differ: %+v", entry)
			}
			return stop
		}
	}
	t.Fatal("own FUSE mount is absent from kernel topology")
	return stop
}

// The client can enter epoll_ctl while opening its FUSE payload. Its daemon
// must run in another process, so answering that callback never depends on
// the client's runtime poller lock or available Go scheduling processor.
func TestSourceFUSEServerProcess(t *testing.T) {
	if os.Getenv("PW_FUSE_FIXTURE_SERVER") != "1" {
		return
	}
	var module []byte
	if os.Getenv("PW_FUSE_FIXTURE_MODULE") == "1" {
		file := os.NewFile(4, "retained-module-input")
		var err error
		module, err = io.ReadAll(io.LimitReader(file, (1<<20)+1))
		_ = file.Close()
		if err != nil || len(module) == 0 || len(module) > 1<<20 {
			os.Exit(2)
		}
	}
	if err := serveSourceFUSE(3, uint32(os.Getuid()), uint32(os.Getgid()), module); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func serveSourceFUSE(fd int, uid, gid uint32, module []byte) error {
	buffer := make([]byte, 1<<20)
	for {
		n, err := unix.Read(fd, buffer)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.ENODEV) || errors.Is(err, unix.EBADF) || n == 0 {
			return nil
		}
		if err != nil {
			return err
		}
		if n < 40 || binary.LittleEndian.Uint32(buffer) != uint32(n) {
			return errors.New("invalid FUSE request size")
		}
		opcode := binary.LittleEndian.Uint32(buffer[4:])
		unique, node := binary.LittleEndian.Uint64(buffer[8:]), binary.LittleEndian.Uint64(buffer[16:])
		if opcode == 2 || opcode == 42 { // FORGET, BATCH_FORGET have no reply.
			continue
		}
		if opcode == 38 { // DESTROY
			return nil
		}
		payload, failure := sourceFUSEReply(opcode, node, buffer[40:n], uid, gid, module)
		reply := make([]byte, 16+len(payload))
		binary.LittleEndian.PutUint32(reply, uint32(len(reply)))
		binary.LittleEndian.PutUint32(reply[4:], uint32(-int32(failure)))
		binary.LittleEndian.PutUint64(reply[8:], unique)
		copy(reply[16:], payload)
		if written, err := unix.Write(fd, reply); err != nil || written != len(reply) {
			return fmt.Errorf("write FUSE reply: %w", err)
		}
	}
}

func sourceFUSEReply(opcode uint32, node uint64, input []byte, uid, gid uint32, module []byte) ([]byte, unix.Errno) {
	switch opcode {
	case 26: // INIT: negotiate v7.31, no optional capabilities.
		out := make([]byte, 64)
		binary.LittleEndian.PutUint32(out, 7)
		binary.LittleEndian.PutUint32(out[4:], 31)
		binary.LittleEndian.PutUint32(out[20:], 64<<10)
		binary.LittleEndian.PutUint32(out[24:], 1)
		return out, 0
	case 1: // LOOKUP

		if node != 1 {
			return nil, unix.ENOENT
		}
		selected := uint64(2)
		if string(input) != "payload\x00" {
			if len(module) == 0 || string(input) != "module.so\x00" {
				return nil, unix.ENOENT
			}
			selected = 3
		}
		out := make([]byte, 128)
		binary.LittleEndian.PutUint64(out, selected)
		copy(out[40:], sourceFUSEAttr(selected, uid, gid, len(module)))
		return out, 0
	case 3: // GETATTR
		out := make([]byte, 104)
		copy(out[16:], sourceFUSEAttr(node, uid, gid, len(module)))
		return out, 0
	case 14, 27: // OPEN, OPENDIR
		return make([]byte, 16), 0
	case 15: // READ
		if len(input) < 20 || (node != 2 && node != 3) {
			return nil, unix.EINVAL
		}
		content := []byte("caller-owned-volume\n")
		if node == 3 {
			content = module
		}
		offset := binary.LittleEndian.Uint64(input[8:])
		if offset >= uint64(len(content)) {
			return nil, 0
		}
		end := min(uint64(len(content)), offset+uint64(binary.LittleEndian.Uint32(input[16:])))
		return content[offset:end], 0
	case 17: // STATFS: read-only fixture capacity; mount flags come from the kernel.
		out := make([]byte, 80)
		binary.LittleEndian.PutUint64(out, 256)
		binary.LittleEndian.PutUint64(out[24:], 4)
		binary.LittleEndian.PutUint32(out[40:], 4096)
		binary.LittleEndian.PutUint32(out[44:], 255)
		binary.LittleEndian.PutUint32(out[48:], 4096)
		return out, 0
	case 28: // READDIR; the chooser enumerates the real volume directory.
		if node != 1 || len(input) < 20 {
			return nil, unix.EINVAL
		}
		names := []string{".", "..", "payload"}
		nodes := []uint64{1, 1, 2}
		if len(module) > 0 {
			names = append(names, "module.so")
			nodes = append(nodes, 3)
		}
		offset := binary.LittleEndian.Uint64(input[8:])
		maximum := int(binary.LittleEndian.Uint32(input[16:]))
		var out []byte
		for index, name := range names {
			if uint64(index) < offset {
				continue
			}
			size := (24 + len(name) + 7) &^ 7
			if len(out)+size > maximum {
				break
			}
			row := make([]byte, size)
			binary.LittleEndian.PutUint64(row, nodes[index])
			binary.LittleEndian.PutUint64(row[8:], uint64(index+1))
			binary.LittleEndian.PutUint32(row[16:], uint32(len(name)))
			kind := uint32(unix.DT_REG)
			if index < 2 {
				kind = unix.DT_DIR
			}
			binary.LittleEndian.PutUint32(row[20:], kind)
			copy(row[24:], name)
			out = append(out, row...)
		}
		return out, 0
	case 18, 20, 25, 29, 30, 34: // RELEASE, FSYNC, FLUSH, RELEASEDIR, FSYNCDIR, ACCESS
		return nil, 0
	default:
		return nil, unix.ENOSYS
	}
}

func sourceFUSEAttr(node uint64, uid, gid uint32, moduleSize int) []byte {
	out := make([]byte, 88)
	binary.LittleEndian.PutUint64(out, node)
	mode, links, size := uint32(unix.S_IFREG|0o644), uint32(1), uint64(len("caller-owned-volume\n"))
	if node == 3 {
		size = uint64(moduleSize)
	}
	if node == 1 {
		mode, links, size = unix.S_IFDIR|0o755, 2, 4096
	}
	binary.LittleEndian.PutUint64(out[8:], size)
	binary.LittleEndian.PutUint32(out[60:], mode)
	binary.LittleEndian.PutUint32(out[64:], links)
	binary.LittleEndian.PutUint32(out[68:], uid)
	binary.LittleEndian.PutUint32(out[72:], gid)
	binary.LittleEndian.PutUint32(out[80:], 4096)
	return out
}
