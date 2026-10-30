//go:build linux

package launchguard

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

const fixtureGuardianProbe = "PW_GUARDIAN_C_PROBE"

func TestGuardianStaticTransitionAuthenticatesRootRoles(t *testing.T) {
	fixture := installLaunchFixture(t)
	probe := buildGuardianCProbe(t)
	testutil.FailErr(t, "exercise actual compiled C with root-selected roles", sudoFixture(t, "guardian-c", fixture.dir, fixtureGuardianProbe+"="+probe, fixtureUIDEnv+"="+strconv.Itoa(os.Getuid()), fixtureGIDEnv+"="+strconv.Itoa(os.Getgid())))
	testutil.FailErr(t, "C fixture released generation lifetime", sudoFixture(t, "lock-free", fixture.dir, fixtureHash+"="+fixture.digest))
}

func buildGuardianCProbe(t *testing.T) string {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "guardian-probe")
	source := filepath.Join("..", "..", "..", "packaging", "linux", "mount-intake")
	command := exec.Command("cc", "-O2", "-std=c11", "-ffreestanding", "-fno-builtin", "-fno-stack-protector", "-fno-pie", "-fno-tree-vectorize", "-nostdlib", "-static", "-no-pie", "-Wl,--no-undefined", "-Wl,-z,noexecstack", "-s", "-I", source, filepath.Join("testdata", "guardianprobe", "main.c"), filepath.Join(source, "entry.S"), "-o", probe)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compile fixed C boundary probe: %v\n%s", err, output)
	}
	testutil.FailErr(t, "make own probe readable to root fixture", os.Chmod(filepath.Dir(probe), 0755))
	return probe
}

type guardianCFixture struct {
	caller       Identity
	roles        [guardianTransitionRoles]*os.File
	image, probe *os.File
	owned        []*os.File
}

func (f *guardianCFixture) close() {
	for _, file := range f.owned {
		_ = file.Close()
	}
}
func (f *guardianCFixture) keep(file *os.File) *os.File { f.owned = append(f.owned, file); return file }

// This entry inspects the C boundary only. The real Go guardian's lineage
// ownership and settlement are exercised by the installed launch tests.
func checkGuardianCTransition(directory, probePath, uidText, gidText string) error {
	runtime.LockOSThread()
	uid, e := strconv.ParseUint(uidText, 10, 32)
	gid, g := strconv.ParseUint(gidText, 10, 32)
	if e != nil || g != nil || uid == 0 {
		return errors.New("invalid C fixture caller")
	}
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return err
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return err
	}
	policyPath, lockDirectory = filepath.Join(directory, "launch.json"), filepath.Join(directory, "locks")
	generation, err := CaptureGeneration()
	if err != nil {
		return err
	}
	defer generation.Close()
	target := filepath.Join(directory, "guardian-probe")
	if _, _, err := copyRootBinary(probePath, target, 0555, nil); err != nil {
		return err
	}
	f := &guardianCFixture{caller: Identity{UID: uint32(uid), GID: uint32(gid), Groups: []uint32{uint32(gid)}}, image: generation.MountTransition}
	defer f.close()
	f.probe, err = os.Open(target)
	if err != nil {
		return err
	}
	f.keep(f.probe)
	frame, err := sealEntrySpec(EntrySpec{Version: 1, SupervisorPID: os.Getpid(), UID: uint32(uid), GID: uint32(gid), Groups: f.caller.Groups, Generation: generation.Policy, Image: generation.DesktopImage})
	if err != nil {
		return err
	}
	f.keep(frame)
	origin, err := os.Open("/proc/self/ns/user")
	if err != nil {
		return err
	}
	f.keep(origin)
	source, err := os.Open("/proc/thread-self/ns/mnt")
	if err != nil {
		return err
	}
	f.keep(source)
	raw, _, err := readCurrentMountTopology()
	if err != nil {
		return err
	}
	opener, err := startMountSource(frame, origin, generation.Entry, raw)
	if err != nil {
		return err
	}
	defer opener.close()
	guard, err := opener.guardianNamespace(origin)
	if err != nil {
		return err
	}
	f.keep(guard)
	if err := opener.finish(); err != nil {
		return err
	}
	children, err := os.Open(fmt.Sprintf("/proc/%d/task/%d/children", os.Getpid(), os.Getpid()))
	if err != nil {
		return err
	}
	f.keep(children)
	proc, err := os.Open("/proc")
	if err != nil {
		return err
	}
	f.keep(proc)
	health, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	healthEndpoint := f.keep(os.NewFile(uintptr(health[0]), "C-health"))
	f.keep(os.NewFile(uintptr(health[1]), "C-health-peer"))
	lifeR, lifeW, err := os.Pipe()
	if err != nil {
		return err
	}
	f.keep(lifeR)
	f.keep(lifeW)
	gateR, gateW, err := os.Pipe()
	if err != nil {
		return err
	}
	f.keep(gateR)
	f.keep(gateW)
	var pids [3]int
	var pins [3]*os.File
	var commands []*exec.Cmd
	defer func() {
		for _, command := range commands {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	for index := range pids {
		child := exec.Command("/bin/sleep", "30")
		fd := -1
		child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: []uint32{uint32(gid)}}, PidFD: &fd}
		if err := child.Start(); err != nil {
			return err
		}
		commands = append(commands, child)
		pids[index] = child.Process.Pid
		pins[index] = f.keep(os.NewFile(uintptr(fd), "C-owned-child-pidfd"))
	}
	roots, err := sealGuardianRoots(guardianRoots{stage: os.Getpid(), leader: os.Getpid(), pids: pids, sequence: 1})
	if err != nil {
		return err
	}
	f.keep(roots)
	f.roles = [guardianTransitionRoles]*os.File{guard, source, frame, generation.Lifetime, healthEndpoint, f.probe, origin, roots, nil, children, proc, pins[0], pins[1], pins[2], lifeW, gateW}
	if err := f.run("ordinary", nil); err != nil {
		return fmt.Errorf("ordinary trace control: %w", err)
	}
	if err := f.run("valid", nil); err != nil {
		return fmt.Errorf("valid C handoff: %w", err)
	}
	for _, name := range []string{"magic", "version", "count", "reserved", "role fd", "role inode", "role reserved", "uid zero", "truncated", "trailing", "unsealed", "foreign proof", "initial guard", "wrong source", "wrong origin", "environment", "argument"} {
		if err := f.run(name, nil); err != nil {
			return fmt.Errorf("C %s refusal: %w", name, err)
		}
	}
	for _, test := range []struct {
		name    string
		owner   uint32
		mapping string
	}{{"foreign namespace owner", uint32(uid), "0 0 4294967295\n"}, {"partial identity map", 0, "0 0 1\n"}} {
		ns, err := f.namespace(test.owner, uint32(gid), test.mapping)
		if err != nil {
			return err
		}
		err = f.run(test.name, ns)
		_ = ns.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func guardianCTestSeal(raw []byte, sealed bool, owner int) (*os.File, error) {
	fd, err := unix.MemfdCreate("guardian-C-boundary", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "C-proof")
	fail := func(err error) (*os.File, error) { _ = file.Close(); return nil, err }
	if _, err := file.Write(raw); err != nil {
		return fail(err)
	}
	if err := file.Chmod(0400); err != nil {
		return fail(err)
	}
	if owner >= 0 {
		if err := file.Chown(owner, -1); err != nil {
			return fail(err)
		}
	}
	if sealed {
		if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, entrySeals); err != nil {
			return fail(err)
		}
	}
	return file, nil
}

func guardianCTestReceive(ctx context.Context, file *os.File) ([]byte, error) {
	raw := make([]byte, 161)
	for {
		n, _, flags, _, err := unix.Recvmsg(int(file.Fd()), raw, nil, unix.MSG_DONTWAIT)
		if err == nil {
			if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || n != 160 {
				return nil, errors.New("invalid C probe report shape")
			}
			return raw[:n], nil
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}

func (f *guardianCFixture) report(raw []byte, pid int, ordinary bool, dump uint32) error {
	word := func(offset int) uint32 { return binary.LittleEndian.Uint32(raw[offset:]) }
	if len(raw) != 160 || string(raw[:8]) != "PWGCPR01" || word(8) != uint32(pid) || word(68) != 2 || word(72) != 1 || word(76) != 1 || word(64) != dump {
		return errors.New("C exec identity/argv/environment/report differs")
	}
	for _, offset := range []int{12, 16, 20} {
		if word(offset) != f.caller.UID {
			return errors.New("C UID differs")
		}
	}
	for _, offset := range []int{24, 28, 32} {
		if word(offset) != f.caller.GID {
			return errors.New("C GID differs")
		}
	}
	if word(96) != 1 || word(100) != f.caller.GID {
		return errors.New("C supplementary groups differ")
	}
	if ordinary {
		return nil
	}
	for offset := 36; offset < 60; offset += 4 {
		if word(offset) != 0 {
			return errors.New("C retained capability set")
		}
	}
	if word(60) != 1 || binary.LittleEndian.Uint64(raw[144:]) != 0 {
		return errors.New("C retained privilege transitions or bounding capabilities")
	}
	var mask uint32
	for _, fd := range []uint{5, 6, 7, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19} {
		mask |= 1 << fd
	}
	if word(80) != mask || word(84) != 0 || word(88) != 0 || word(92) != 0 {
		return errors.New("C exec descriptor inventory differs")
	}
	stat, err := f.roles[0].Stat()
	if err != nil {
		return err
	}
	kernel := stat.Sys().(*syscall.Stat_t)
	if word(132) != 0 || binary.LittleEndian.Uint64(raw[136:]) != kernel.Ino {
		return errors.New("C entered a different namespace/owner")
	}
	return nil
}

func (f *guardianCFixture) run(name string, alternate *os.File) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	parent, child := os.NewFile(uintptr(pair[0]), "C-control-parent"), os.NewFile(uintptr(pair[1]), "C-control-child")
	defer parent.Close()
	defer child.Close()
	roles := f.roles
	roles[8] = child
	switch name {
	case "initial guard":
		roles[0] = roles[6]
	case "wrong source":
		roles[1] = roles[6]
	case "wrong origin":
		roles[6] = roles[0]
	}
	if alternate != nil {
		roles[0] = alternate
	}
	proof, err := sealGuardianTransition(roles, f.caller)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.NewSectionReader(proof, 0, guardianTransitionHeader+4))
	_ = proof.Close()
	if err != nil {
		return err
	}
	switch name {
	case "magic":
		raw[0] ^= 1
	case "version":
		raw[8] = 2
	case "count":
		raw[12] = 15
	case "reserved":
		raw[412] = 1
	case "role fd":
		raw[16] = 4
	case "role inode":
		raw[32] ^= 1
	case "role reserved":
		raw[20] = 1
	case "uid zero":
		binary.LittleEndian.PutUint32(raw[400:], 0)
	case "truncated":
		raw = raw[:len(raw)-1]
	case "trailing":
		raw = append(raw, 0)
	}
	owner := -1
	if name == "foreign proof" {
		owner = int(f.caller.UID)
	}
	proof, err = guardianCTestSeal(raw, name != "unsealed", owner)
	if err != nil {
		return err
	}
	defer proof.Close()
	extra := make([]*os.File, guardianIncomingImage-2)
	for index, file := range roles {
		extra[guardianRoleFD(index)+guardianIncomingBase-3] = file
	}
	extra[guardianIncomingProof-3], extra[guardianIncomingImage-3] = proof, f.image
	command := exec.CommandContext(ctx, "/proc/self/fd/"+strconv.Itoa(guardianIncomingImage))
	command.Args = []string{"pw-mount-intake-transition"}
	command.Env = []string{}
	command.ExtraFiles = extra
	ordinary := name == "ordinary"
	if ordinary {
		command = exec.CommandContext(ctx, f.probe.Name(), "--ordinary-target")
		command.Args[0] = "pw-launch-entry"
		command.Env = []string{}
		command.ExtraFiles = make([]*os.File, 10)
		command.ExtraFiles[9] = child
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: f.caller.UID, Gid: f.caller.GID, Groups: f.caller.Groups}}
	}
	if name == "environment" {
		command.Env = []string{"UNTRUSTED_INPUT=1"}
	}
	if name == "argument" {
		command.Args = append(command.Args, "unexpected")
	}
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		return err
	}
	waited := false
	defer func() {
		if !waited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	if name != "valid" && !ordinary {
		err := command.Wait()
		waited = true
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 125 || output.Len() != 0 {
			return fmt.Errorf("C refusal status=%w output=%q", err, output.String())
		}
		buffer := make([]byte, 161)
		n, _, _, _, e := unix.Recvmsg(int(parent.Fd()), buffer, nil, unix.MSG_DONTWAIT)
		if n > 0 || (!errors.Is(e, unix.EAGAIN) && e != nil) {
			return errors.New("refused C handoff executed probe")
		}
		return nil
	}
	first, err := guardianCTestReceive(ctx, parent)
	if err != nil {
		return err
	}
	if err := f.report(first, command.Process.Pid, ordinary, 1); err != nil {
		return err
	}
	mode := "--trace-refuse"
	if ordinary {
		mode = "--trace-allow"
	}
	tracer := exec.CommandContext(ctx, f.probe.Name(), mode, strconv.Itoa(command.Process.Pid))
	tracer.Env = []string{}
	tracer.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: f.caller.UID, Gid: f.caller.GID, Groups: f.caller.Groups}}
	if err := tracer.Run(); err != nil {
		return fmt.Errorf("outside caller %s: %w", mode, err)
	}
	if _, err := parent.Write([]byte{2}); err != nil {
		return err
	}
	final, err := guardianCTestReceive(ctx, parent)
	if err != nil {
		return err
	}
	if err := f.report(final, command.Process.Pid, ordinary, 0); err != nil {
		return err
	}
	err = command.Wait()
	waited = true
	if err != nil || output.Len() != 0 {
		return fmt.Errorf("C positive completion=%w output=%q", err, output.String())
	}
	return nil
}

func (f *guardianCFixture) namespace(owner, gid uint32, mapping string) (_ *os.File, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parent, child := os.NewFile(uintptr(pair[0]), "C-ns-parent"), os.NewFile(uintptr(pair[1]), "C-ns-child")
	defer parent.Close()
	defer child.Close()
	command := exec.CommandContext(ctx, f.probe.Name(), "--namespace-factory", strconv.FormatUint(uint64(owner), 10), strconv.FormatUint(uint64(gid), 10))
	command.Env = []string{}
	command.ExtraFiles = []*os.File{child}
	if err := command.Start(); err != nil {
		return nil, err
	}
	waited := false
	defer func() {
		if !waited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	if err := unix.SetsockoptTimeval(int(parent.Fd()), unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 2}); err != nil {
		return nil, err
	}
	ready := make([]byte, 1)
	n, _, _, _, err := unix.Recvmsg(int(parent.Fd()), ready, nil, 0)
	if err != nil || n != 1 || ready[0] != 1 {
		return nil, errors.New("owned namespace factory did not become ready")
	}
	directory := filepath.Join("/proc", strconv.Itoa(command.Process.Pid))
	// The original owned child cannot be reaped/reused before this sole Wait.
	for _, name := range []string{"uid_map", "gid_map"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(mapping), 0600); err != nil {
			return nil, err
		}
	}
	file, err := os.Open(filepath.Join(directory, "ns/user"))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = file.Close()
		}
	}()
	actual, err := unix.IoctlGetInt(int(file.Fd()), unix.NS_GET_OWNER_UID)
	if err != nil || actual != int(owner) {
		return nil, errors.New("namespace factory owner differs")
	}
	if _, err := parent.Write([]byte{2}); err != nil {
		return nil, err
	}
	err = command.Wait()
	waited = true
	if err != nil {
		return nil, err
	}
	return file, nil
}
