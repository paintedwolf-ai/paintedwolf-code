//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

type privateModuleView struct {
	watcher   *exec.Cmd
	intake    *exec.Cmd
	lifetime  *os.File
	health    *os.File
	namespace *os.File
	setup     *mountViewSetup
	pidfds    [2]*os.File
}

// The stage authenticates both fixed images and prepares every descriptor.
// Only the static transition joins U_view; Go never joins a user namespace.
func startPrivateModuleView(frame, entry, origin, transition *os.File, prepared *preparedModuleView) (_ *privateModuleView, err error) {
	plan, err := sealModuleViewPlan(prepared.plan)
	if err != nil {
		return nil, err
	}
	defer func() { _ = plan.Close() }()
	lifeR, lifeW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer func() { _ = lifeR.Close() }()
	view := &privateModuleView{lifetime: lifeW}
	defer func() {
		if err != nil {
			view.abort()
		}
	}()
	stagePair, err := mountSocketPair("mount-view-stage", "mount-view-writer")
	if err != nil {
		return nil, err
	}
	view.health = stagePair[0]
	defer func() { _ = stagePair[1].Close() }()
	intakePair, err := mountSocketPair("mount-view-intake", "mount-intake-source")
	if err != nil {
		return nil, err
	}
	defer func() { _ = intakePair[0].Close(); _ = intakePair[1].Close() }()
	if err := sendMountTransfer(int(intakePair[0].Fd()), mountTransfer{kind: mountTransferPlan, sequence: 1}, int(plan.Fd())); err != nil {
		return nil, err
	}
	view.setup = newMountViewSetup(int(stagePair[0].Fd()))
	defer func() { err = errors.Join(err, view.setup.stop()) }()
	watcherPIDFD := -1
	intakePIDFD := -1
	view.watcher = &exec.Cmd{
		Path: "/proc/self/fd/" + strconv.Itoa(viewSelfFD), Args: []string{"pw-launch-entry", MountViewArgument}, Env: []string{},
		ExtraFiles: []*os.File{frame, lifeR, stagePair[1], entry, plan, intakePair[0], origin},
		SysProcAttr: &syscall.SysProcAttr{
			PidFD:                      &watcherPIDFD,
			Cloneflags:                 unix.CLONE_NEWUSER | unix.CLONE_NEWNS,
			UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: int(^uint32(0))}},
			GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: int(^uint32(0))}},
			GidMappingsEnableSetgroups: true,
		},
	}
	if err := view.setup.start(view.watcher); err != nil {
		return nil, fmt.Errorf("start private module writer: %w", err)
	}
	view.pidfds[0] = os.NewFile(uintptr(watcherPIDFD), "mount-writer-pidfd")
	_ = stagePair[1].Close()
	if err := transferInitialMountLeases(int(stagePair[0].Fd()), prepared); err != nil {
		return nil, err
	}
	stageSequence := uint64(len(prepared.plan.initial)) + 1
	if err := expectMountViewReady(int(stagePair[0].Fd()), stageSequence); err != nil {
		return nil, err
	}
	view.namespace, err = os.Open(fmt.Sprintf("/proc/%d/ns/mnt", view.watcher.Process.Pid))
	if err != nil {
		return nil, err
	}
	user, err := os.Open(fmt.Sprintf("/proc/%d/ns/user", view.watcher.Process.Pid))
	if err != nil {
		return nil, err
	}
	defer func() { _ = user.Close() }()
	roles := [7]*os.File{user, prepared.source, frame, lifeR, intakePair[1], entry, origin}
	proof, err := sealMountTransition(roles)
	if err != nil {
		return nil, err
	}
	defer func() { _ = proof.Close() }()
	files := append([]*os.File{}, roles[:]...)
	files = append(files, proof, transition)
	view.intake = &exec.Cmd{Path: "/proc/self/fd/11", Args: []string{"pw-mount-intake-transition"}, Env: []string{}, ExtraFiles: files, SysProcAttr: &syscall.SysProcAttr{PidFD: &intakePIDFD}}
	if err := view.setup.start(view.intake); err != nil {
		return nil, fmt.Errorf("start fixed module intake: %w", err)
	}
	view.pidfds[1] = os.NewFile(uintptr(intakePIDFD), "mount-intake-pidfd")
	_ = intakePair[1].Close()
	_ = intakePair[0].Close()
	if err := expectMountViewReady(int(stagePair[0].Fd()), stageSequence+1); err != nil {
		return nil, err
	}
	return view, nil
}

func mountSocketPair(a, b string) ([2]*os.File, error) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return [2]*os.File{}, err
	}
	return [2]*os.File{os.NewFile(uintptr(pair[0]), a), os.NewFile(uintptr(pair[1]), b)}, nil
}

func expectMountViewReady(socket int, sequence uint64) error {
	message, fd, err := receiveMountTransfer(socket)
	if fd >= 0 {
		_ = unix.Close(fd)
	}
	if err != nil || fd >= 0 || message != (mountTransfer{kind: mountTransferReady, sequence: sequence}) {
		return errors.New("private module view did not reconcile before readiness")
	}
	return nil
}

func (view *privateModuleView) abort() {
	defer func() {
		for _, file := range view.pidfds {
			if file != nil {
				_ = file.Close()
			}
		}
	}()
	if view.lifetime != nil {
		_ = view.lifetime.Close()
	}
	if view.setup != nil {
		view.setup.abort()
	}
	if view.health != nil {
		_ = view.health.Close()
	}
	if view.namespace != nil {
		_ = view.namespace.Close()
	}
}
