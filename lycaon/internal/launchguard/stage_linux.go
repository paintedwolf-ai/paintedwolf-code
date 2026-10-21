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
		return fmt.Errorf("start protected entry: %w", err)
	}
	agent := &exec.Cmd{
		Path:       "/proc/self/fd/" + strconv.Itoa(agentSelfFD),
		Args:       []string{"pw-launch-entry", BrokerAgentArgument},
		Env:        []string{},
		Stderr:     os.Stderr,
		ExtraFiles: []*os.File{frame, os.NewFile(stageServerFD, "launch-broker"), entryImage},
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

// agentSelfFD is where the broker agent's image sits for its execution.
const agentSelfFD = agentBrokerFD + 1
