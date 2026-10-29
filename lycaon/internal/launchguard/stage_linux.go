//go:build linux

package launchguard

import (
	"errors"
	"fmt"
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
// generation, starts the broker agent as uid 0 so the agent clears dumpable
// and drops privileges itself without a dumpable exec window, then clones the
// unprivileged entry into the protected user namespace as the original
// caller; both children are reparented to the supervisor when the stage exits.
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
	originFile := os.NewFile(stageOriginFD, "original-user-namespace")
	brokerFile := os.NewFile(stageBrokerFD, "desktop-broker")
	lifetimeFile := os.NewFile(stageLifetimeFD, "generation-lifetime")
	serverFile := os.NewFile(stageServerFD, "launch-broker")
	defer func() {
		runtime.KeepAlive(mounted)
		runtime.KeepAlive(frame)
		runtime.KeepAlive(originFile)
		runtime.KeepAlive(brokerFile)
		runtime.KeepAlive(lifetimeFile)
		runtime.KeepAlive(entryImage)
		runtime.KeepAlive(serverFile)
	}()
	null, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() { _ = null.Close() }()
	// The broker agent starts as uid 0 in the initial user namespace and drops
	// to the caller's identity after execve, so the kernel never resets its
	// dumpable flag to 1 under the caller's credentials. It starts before the
	// entry so no desktop ever runs without a broker: an agent that cannot
	// start refuses the launch, and an entry that cannot start ends the agent
	// when the bootstrap closes the desktop's end of the socket.
	agent := &exec.Cmd{
		Path:       "/proc/self/fd/" + strconv.Itoa(agentSelfFD),
		Args:       []string{"pw-launch-entry", BrokerAgentArgument},
		Env:        []string{},
		Stdin:      null,
		Stdout:     null,
		Stderr:     os.Stderr,
		ExtraFiles: []*os.File{frame, serverFile, entryImage},
		SysProcAttr: &syscall.SysProcAttr{
			Setsid: true,
		},
	}
	if err := agent.Start(); err != nil {
		return fmt.Errorf("start broker agent: %w", err)
	}
	entry := &exec.Cmd{
		Path:   "/proc/self/fd/" + strconv.Itoa(entrySelfFD),
		Args:   []string{"pw-launch-entry"},
		Env:    []string{},
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		ExtraFiles: []*os.File{
			mounted, frame,
			originFile,
			brokerFile,
			lifetimeFile,
			entryImage,
		},
		SysProcAttr: attributes,
	}
	if err := entry.Start(); err != nil {
		return fmt.Errorf("start protected entry: %w", err)
	}
	return nil
}

// agentSelfFD is where the broker agent's image sits for its execution.
const agentSelfFD = agentBrokerFD + 1
