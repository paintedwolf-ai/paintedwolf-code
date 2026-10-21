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

// entrySelfFD is where the entry's own image sits for its execution; the
// entry marks it close-on-exec with every other bootstrap descriptor.
const entrySelfFD = entryLifetimeFD + 1

// RunStage is the privileged stage: uid 0 of the initial user namespace,
// cloned into a fresh mount namespace by the bootstrap. It binds the selected
// generation, then clones the unprivileged entry into the protected user
// namespace as the original caller and exits; the entry is reparented to the
// supervisor. It reads nothing a caller supplied except through the sealed
// root-created frame.
func RunStage() error {
	if uid, euid, suid := unix.Getresuid(); uid != 0 || euid != 0 || suid != 0 {
		return errors.New("protected stage requires the bootstrap's administrator identity")
	}
	if gid, egid, sgid := unix.Getresgid(); gid != 0 || egid != 0 || sgid != 0 {
		return errors.New("protected stage requires the bootstrap's administrator identity")
	}
	// Descriptors arrive without close-on-exec; each child receives only the
	// ones handed to it below.
	if err := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return err
	}
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
	attributes, err := EntryAttributes(Identity{UID: spec.UID, GID: spec.GID, Groups: spec.Groups})
	if err != nil {
		return err
	}
	entryImage := os.NewFile(stageEntryFD, "selected-entry-image")
	if err := rootRegular(entryImage); err != nil {
		return err
	}
	// The entry and the module-view watcher start in the module view; the
	// broker agent starts back in the launch's own mount namespace and holds
	// the watcher's lifetime pipe until the launch ends.
	runtime.LockOSThread()
	leave, err := enterModuleView(spec.Generation, mounted)
	if err != nil {
		return err
	}
	watchLifetime, err := startModuleViewWatcher(frame, entryImage)
	if err != nil {
		_ = leave()
		return err
	}
	defer func() { _ = watchLifetime.Close() }()
	viewed, err := reopenImage(spec.Generation, mounted)
	if err != nil {
		_ = leave()
		return err
	}
	_ = mounted.Close()
	mounted = viewed
	entry := &exec.Cmd{
		Path:   "/proc/self/fd/" + strconv.Itoa(entrySelfFD),
		Args:   []string{"pw-launch-entry"},
		Env:    []string{},
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		ExtraFiles: []*os.File{
			mounted, frame,
			os.NewFile(stageOriginFD, "original-user-namespace"),
			os.NewFile(stageBrokerFD, "desktop-broker"),
			os.NewFile(stageLifetimeFD, "generation-lifetime"),
			entryImage,
		},
		SysProcAttr: attributes,
	}
	if err := entry.Start(); err != nil {
		_ = leave()
		return fmt.Errorf("start protected entry: %w", err)
	}
	if err := leave(); err != nil {
		return fmt.Errorf("leave module view: %w", err)
	}
	agent := &exec.Cmd{
		Path:       "/proc/self/fd/" + strconv.Itoa(agentSelfFD),
		Args:       []string{"pw-launch-entry", BrokerAgentArgument},
		Env:        []string{},
		Stderr:     os.Stderr,
		ExtraFiles: []*os.File{frame, os.NewFile(stageServerFD, "launch-broker"), watchLifetime, entryImage},
		SysProcAttr: &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: spec.UID, Gid: spec.GID, Groups: spec.Groups},
			Setsid:     true,
		},
	}
	if err := agent.Start(); err != nil {
		return fmt.Errorf("start broker agent: %w", err)
	}
	return nil
}

// startModuleViewWatcher starts the module-view mount watcher in the current
// mount namespace and waits until its baseline scan is armed.
func startModuleViewWatcher(frame, entryImage *os.File) (*os.File, error) {
	watchR, watchW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		_ = watchR.Close()
		_ = watchW.Close()
		return nil, err
	}
	watcher := &exec.Cmd{
		Path:       "/proc/self/fd/" + strconv.Itoa(watcherSelfFD),
		Args:       []string{"pw-launch-entry", BrokerAgentArgument},
		Env:        []string{},
		Stderr:     os.Stderr,
		ExtraFiles: []*os.File{frame, watchR, readyW, entryImage},
		SysProcAttr: &syscall.SysProcAttr{
			Cloneflags:                 unix.CLONE_NEWUSER | unix.CLONE_NEWNS,
			UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: int(^uint32(0))}},
			GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: int(^uint32(0))}},
			GidMappingsEnableSetgroups: true,
		},
	}
	err = watcher.Start()
	_ = watchR.Close()
	_ = readyW.Close()
	if err != nil {
		_ = readyR.Close()
		_ = watchW.Close()
		return nil, fmt.Errorf("start module view watcher: %w", err)
	}
	var ready [1]byte
	_, err = io.ReadFull(readyR, ready[:])
	_ = readyR.Close()
	if err != nil {
		_ = watchW.Close()
		return nil, fmt.Errorf("module view watcher did not become ready: %w", err)
	}
	viewNS, err := os.Open(fmt.Sprintf("/proc/%d/ns/mnt", watcher.Process.Pid))
	if err != nil {
		_ = watchW.Close()
		return nil, fmt.Errorf("open module view mount namespace: %w", err)
	}
	err = unix.Setns(int(viewNS.Fd()), unix.CLONE_NEWNS)
	_ = viewNS.Close()
	if err != nil {
		_ = watchW.Close()
		return nil, fmt.Errorf("enter module view mount namespace: %w", err)
	}
	return watchW, nil
}

// agentSelfFD is where the broker agent's image sits for its execution.
const agentSelfFD = agentWatchFD + 1
