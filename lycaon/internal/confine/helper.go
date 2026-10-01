//go:build !windows

package confine

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/lineage"
)

func runHelperIfInvoked() {
	if !IsHelperInvocation(os.Args) {
		return
	}
	if err := runHelper(os.Args[2:]); err != nil {
		fmt.Fprint(os.Stderr, HelperStderrPrefix+err.Error()+"\n")
		if IsCommandNotFound(err) {
			os.Exit(CommandNotFoundExit)
		}
		os.Exit(HelperFailureExit)
	}
}

// runHelper applies an inherited profile before replacing the process.
func runHelper(args []string) error {
	profileFD, lineageFD := -1, -1
	var target []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case profileFDFlag, lineageFDFlag:
			flag := args[i]
			if i+1 >= len(args) {
				return fmt.Errorf("%s requires a file descriptor", flag)
			}
			fd, err := strconv.Atoi(args[i+1])
			if err != nil || fd < 0 {
				return fmt.Errorf("%s requires a file descriptor, got %q", flag, args[i+1])
			}
			if flag == profileFDFlag {
				profileFD = fd
			} else {
				lineageFD = fd
			}
			i++
		case "--":
			target = args[i+1:]
			i = len(args)
		}
	}
	if profileFD < 0 || len(target) == 0 {
		return fmt.Errorf("usage: %s %s <fd> -- <cmd> [args...]", helperFlag, profileFDFlag)
	}

	// The inherited pipe keeps profile bytes outside writable paths.
	pipe := os.NewFile(uintptr(profileFD), "confine-profile")
	if pipe == nil {
		return fmt.Errorf("invalid profile descriptor %d", profileFD)
	}
	profile, err := io.ReadAll(pipe)
	_ = pipe.Close()
	if err != nil {
		return fmt.Errorf("read profile: %w", err)
	}
	if len(profile) == 0 {
		return fmt.Errorf("empty sandbox profile")
	}

	// Move the marker to its documented descriptor before exec, clear of the
	// ones shells assign themselves.
	if lineageFD >= 0 {
		if err := placeLineageMarker(lineageFD); err != nil {
			return err
		}
	}

	// Resolve PATH before applying the profile.
	name := target[0]
	resolved := name
	if !strings.ContainsRune(name, '/') {
		p, err := exec.LookPath(name)
		if err != nil {
			return &CommandNotFoundError{Name: name}
		}
		resolved = p
	}

	if err := applySeatbelt(string(profile)); err != nil {
		return err
	}
	// Confirm kernel membership before transferring control.
	if err := attestConfined(); err != nil {
		return err
	}
	if err := syscall.Exec(resolved, target, os.Environ()); err != nil {
		return ClassifyExecError(resolved, err)
	}
	return nil
}

// placeLineageMarker moves the inherited marker to lineage.ChildFD and clears
// close-on-exec so every descendant keeps it.
func placeLineageMarker(fd int) error {
	if fd != lineage.ChildFD {
		if err := syscall.Dup2(fd, lineage.ChildFD); err != nil {
			return fmt.Errorf("place lineage marker: %w", err)
		}
		_ = syscall.Close(fd)
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(lineage.ChildFD), syscall.F_SETFD, 0); errno != 0 {
		return fmt.Errorf("place lineage marker: %w", errno)
	}
	return nil
}
