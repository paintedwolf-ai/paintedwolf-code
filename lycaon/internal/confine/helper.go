//go:build !windows

package confine

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
	"github.com/lycaon/lycaon/internal/lineage"
)

func runHelperIfInvoked() {
	if !IsHelperInvocation(os.Args) {
		return
	}
	if err := runHelper(os.Args[2:]); err != nil {
		var unavailable interface{ UnavailableJSON() string }
		if errors.As(err, &unavailable) {
			// The typed reason lets the host say what the kernel lacks.
			fmt.Fprint(os.Stderr, HelperStderrPrefix+linuxsandbox.UnavailableMarker+unavailable.UnavailableJSON()+"\n")
			exitHelper(HelperUnavailableExit)
		}
		fmt.Fprint(os.Stderr, HelperStderrPrefix+err.Error()+"\n")
		if IsCommandNotFound(err) {
			exitHelper(CommandNotFoundExit)
		}
		exitHelper(HelperFailureExit)
	}
}

// exitHelper ends a helper process with code.
func exitHelper(code int) {
	linuxsandbox.FlushCoverage()
	os.Exit(code)
}

// helperRefusalFDArg is the refusal pipe this helper was given, or -1.
var helperRefusalFDArg = -1
var helperPinnedCwdFDArg = -1
var helperPinnedReadyFDArg = -1
var helperPinnedEntryArg string
var helperPinnedExecutableFDArg = -1
var helperPinnedFinal bool
var helperPinnedHostExecution bool

// runHelper applies an inherited profile before replacing the process.
func runHelper(args []string) error {
	profileFD, lineageFD := -1, -1
	helperRefusalFDArg = -1
	helperPinnedCwdFDArg, helperPinnedReadyFDArg = -1, -1
	helperPinnedEntryArg = ""
	helperPinnedExecutableFDArg = -1
	helperPinnedFinal = false
	helperPinnedHostExecution = false
	var target []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case profileFDFlag, lineageFDFlag, refusalFDFlag, pinCwdFDFlag, pinReadyFDFlag, pinExecutableFDFlag:
			flag := args[i]
			if i+1 >= len(args) {
				return fmt.Errorf("%s requires a file descriptor", flag)
			}
			fd, err := strconv.Atoi(args[i+1])
			if err != nil || fd < 0 {
				return fmt.Errorf("%s requires a file descriptor, got %q", flag, args[i+1])
			}
			switch flag {
			case profileFDFlag:
				profileFD = fd
			case lineageFDFlag:
				lineageFD = fd
			case pinCwdFDFlag:
				helperPinnedCwdFDArg = fd
			case pinReadyFDFlag:
				helperPinnedReadyFDArg = fd
			case pinExecutableFDFlag:
				helperPinnedExecutableFDArg = fd
			default:
				helperRefusalFDArg = fd
			}
			i++
		case pinEntryFlag:
			if i+1 >= len(args) || args[i+1] == "" {
				return fmt.Errorf("pinned entry is required")
			}
			helperPinnedEntryArg = args[i+1]
			i++
		case pinFinalFlag:
			helperPinnedFinal = true
		case pinHostExecutionFlag:
			helperPinnedHostExecution = true
		case "--":
			target = args[i+1:]
			i = len(args)
		}
	}
	if helperPinnedFinal {
		if helperPinnedEntryArg == "" || helperPinnedHostExecution || helperPinnedExecutableFDArg < 0 || helperPinnedCwdFDArg < 0 || helperPinnedReadyFDArg < 0 || len(target) == 0 {
			return fmt.Errorf("invalid inherited final launch descriptors")
		}
		if lineageFD >= 0 {
			if err := placeLineageMarker(lineageFD); err != nil {
				return err
			}
		}
		return execPinnedTarget(helperPinnedEntryArg, target, helperPinnedCwdFDArg, helperPinnedReadyFDArg)
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
	if len(profile) == 0 && !helperPinnedHostExecution {
		return fmt.Errorf("empty sandbox profile")
	}

	if helperPinnedReadyFDArg >= 0 {
		defer func() {
			if helperPinnedReadyFDArg >= 0 {
				_ = syscall.Close(helperPinnedReadyFDArg)
			}
		}()
	}
	if helperPinnedReadyFDArg >= 0 || helperPinnedCwdFDArg >= 0 || helperPinnedEntryArg != "" || helperPinnedHostExecution {
		if helperPinnedReadyFDArg < 0 || helperPinnedCwdFDArg < 0 || helperPinnedEntryArg == "" {
			return fmt.Errorf("pinned helper requires complete descriptor handoff")
		}
	}

	// Move the marker to its documented descriptor before exec, clear of the
	// ones shells assign themselves.
	if lineageFD >= 0 {
		if err := placeLineageMarker(lineageFD); err != nil {
			return err
		}
	}

	// Resolve PATH before applying the boundary.
	name := target[0]
	resolved := name
	if helperPinnedEntryArg != "" && !strings.ContainsRune(name, '/') {
		resolved = helperPinnedEntryArg
	} else if !strings.ContainsRune(name, '/') {
		p, err := exec.LookPath(name)
		if err != nil {
			return &CommandNotFoundError{Name: name}
		}
		resolved = p
	}
	if helperPinnedHostExecution {
		// The host selected explicit host execution before constructing this helper.
		// This never removes an inherited confinement boundary.
		return execPinnedTarget(resolved, target, helperPinnedCwdFDArg, helperPinnedReadyFDArg)
	}
	// The platform applies the boundary, confirms it, and runs the target.
	return applyAndExec(profile, resolved, target)
}

// placeLineageMarker moves the inherited marker to lineage.ChildFD and clears
// close-on-exec so every descendant keeps it.
func placeLineageMarker(fd int) error {
	if fd != lineage.ChildFD {
		// linux/arm64 has no dup2 syscall; unix.Dup2 uses dup3 there.
		if err := unix.Dup2(fd, lineage.ChildFD); err != nil {
			return fmt.Errorf("place lineage marker: %w", err)
		}
		_ = syscall.Close(fd)
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(lineage.ChildFD), syscall.F_SETFD, 0); errno != 0 {
		return fmt.Errorf("place lineage marker: %w", errno)
	}
	return nil
}
