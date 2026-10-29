//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

const entrySelfFD = entryLifetimeFD + 1
const agentSelfFD = agentWatchFD + 1

// RunStage creates the private view and its protected-only child lineage.
// Its privileged phase ends with same-PID exec through the authenticated
// static transition into the caller guardian. Ordinary broker launches are
// parented outside that lineage, directly to the bootstrap supervisor.
func RunStage() error {
	runtime.LockOSThread()
	if err := initialUserNamespace(); err != nil {
		return err
	}
	if uid, effective, saved := unix.Getresuid(); uid != 0 || effective != 0 || saved != 0 {
		return errors.New("protected stage requires the bootstrap's administrator identity")
	}
	if gid, effective, saved := unix.Getresgid(); gid != 0 || effective != 0 || saved != 0 {
		return errors.New("protected stage requires the bootstrap's administrator group identity")
	}
	if unix.Gettid() != os.Getpid() {
		return errors.New("protected stage must run on the original process leader")
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	if err := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return err
	}
	slots, err := reserveGuardianExecSlots()
	if err != nil {
		return err
	}
	defer slots.close()
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return err
	}
	children, err := os.Open(fmt.Sprintf("/proc/%d/task/%d/children", os.Getpid(), unix.Gettid()))
	if err != nil {
		return err
	}
	defer func() { _ = children.Close() }()
	proc, err := os.Open("/proc")
	if err != nil {
		return err
	}
	defer func() { _ = proc.Close() }()
	frame := os.NewFile(stageFrameFD, "sealed-entry-spec")
	spec, err := readEntry(frame)
	if err != nil {
		return err
	}
	generation := os.NewFile(stageGenerationFD, "selected-generation-root")
	desktop := os.NewFile(stageDesktopFD, "selected-desktop-image")
	mounted, err := bindGeneration(spec.Generation, generation, desktop)
	if err != nil {
		return err
	}
	defer func() { _ = mounted.Close() }()
	_ = generation.Close()
	_ = desktop.Close()
	entryImage := os.NewFile(stageEntryFD, "selected-entry-image")
	origin := os.NewFile(stageOriginFD, "original-user-namespace")
	transition := os.NewFile(stageMountTransitionFD, "selected-mount-transition")
	lifetime := os.NewFile(stageLifetimeFD, "generation-lifetime")
	handoff := os.NewFile(stageGuardianFD, "guardian-bootstrap-handoff")
	if err := rootRegular(entryImage); err != nil {
		return err
	}
	if err := rootRegular(transition); err != nil {
		return err
	}
	prepared, err := prepareModuleView(spec, frame, origin, entryImage, mounted)
	if err != nil {
		return err
	}
	defer prepared.close()
	if unix.Gettid() != os.Getpid() {
		return errors.New("protected stage lost leader before mount workers")
	}
	view, err := startPrivateModuleView(frame, entryImage, origin, transition, prepared)
	if err != nil {
		return err
	}
	defer view.abort()
	if err := unix.Setns(int(view.namespace.Fd()), unix.CLONE_NEWNS); err != nil {
		return err
	}
	viewed, err := reopenImage(spec.Generation, mounted)
	if err != nil {
		return err
	}
	_ = mounted.Close()
	mounted = viewed
	gateR, gateW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = gateR.Close(); _ = gateW.Close() }()
	attributes, err := EntryAttributes(Identity{UID: spec.UID, GID: spec.GID, Groups: spec.Groups})
	if err != nil {
		return err
	}
	entryPIDFD := -1
	attributes.PidFD = &entryPIDFD
	entry := &exec.Cmd{Path: "/proc/self/fd/" + strconv.Itoa(entrySelfFD), Args: []string{"pw-launch-entry"}, Env: []string{}, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		ExtraFiles: []*os.File{mounted, frame, origin, os.NewFile(stageBrokerFD, "desktop-broker"), lifetime, entryImage, gateR}, SysProcAttr: attributes}
	if unix.Gettid() != os.Getpid() {
		return errors.New("protected stage lost leader before desktop")
	}
	if err := view.setup.start(entry); err != nil {
		return fmt.Errorf("start protected entry: %w", err)
	}
	_ = gateR.Close()
	entryPin := os.NewFile(uintptr(entryPIDFD), "protected-desktop-pidfd")
	defer func() { _ = entryPin.Close() }()
	// Ordinary programs see the original launch view, never the private app
	// namespace. No namespace-transition descriptor reaches their broker.
	if err := unix.Setns(int(prepared.source.Fd()), unix.CLONE_NEWNS); err != nil {
		return err
	}
	if err := startOrdinaryBroker(frame, entryImage, lifetime); err != nil {
		return err
	}
	roots, err := sealGuardianRoots(guardianRoots{stage: os.Getpid(), leader: unix.Gettid(), pids: [3]int{entry.Process.Pid, view.watcher.Process.Pid, view.intake.Process.Pid}, sequence: uint64(len(prepared.plan.initial)) + 3})
	if err != nil {
		return err
	}
	defer func() { _ = roots.Close() }()
	roles := [guardianTransitionRoles]*os.File{prepared.guardian, prepared.source, frame, lifetime, view.health, entryImage, origin, roots, handoff, children, proc, entryPin, view.pidfds[0], view.pidfds[1], view.lifetime, gateW}
	proof, err := sealGuardianTransition(roles, Identity{UID: spec.UID, GID: spec.GID, Groups: spec.Groups})
	if err != nil {
		return err
	}
	defer func() { _ = proof.Close() }()
	return slots.execute(roles, proof, transition)
}

// The readiness marker is sent only after the broker has dropped its initial
// root identity on every thread. It does not retain the private view lifetime.
func startOrdinaryBroker(frame, entry, lifetime *os.File) error {
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = readyR.Close(); _ = readyW.Close() }()
	null, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() { _ = null.Close() }()
	agent := &exec.Cmd{Path: "/proc/self/fd/" + strconv.Itoa(agentSelfFD), Args: []string{"pw-launch-entry", BrokerAgentArgument}, Env: []string{}, Stdin: null, Stdout: null, Stderr: null,
		ExtraFiles: []*os.File{frame, os.NewFile(stageServerFD, "launch-broker"), lifetime, entry, readyW}, SysProcAttr: &syscall.SysProcAttr{Cloneflags: unix.CLONE_PARENT, Setsid: true}}
	if err := agent.Start(); err != nil {
		return fmt.Errorf("start ordinary launch broker: %w", err)
	}
	_ = readyW.Close()
	_ = agent.Process.Release() // Its actual parent, the bootstrap, owns reaping.
	marker, err := io.ReadAll(io.LimitReader(readyR, 2))
	if err != nil || len(marker) != 1 || marker[0] != 1 {
		return errors.New("ordinary broker did not drop its initial-root identity")
	}
	return nil
}
