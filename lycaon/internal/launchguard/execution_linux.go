//go:build linux

package launchguard

import (
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// restrictExecution confines execve for the whole protected namespace to the
// selected generation, the desktop image's program interpreter, and the
// system executables the administrator's policy names (such as WebKit's
// helper directory). Anything else the desktop, the engine or a library they
// load tries to execute fails, so no program outside that closure can start
// beside the protected processes; programs that must run leave through the
// broker. The domain is applied to the calling thread, which then executes
// the desktop, and every descendant inherits it.
func restrictExecution(policy GenerationPolicy, image *os.File) error {
	paths := []string{policy.LogicalInstallRoot}
	interpreter, err := programInterpreter(image)
	if err != nil {
		return err
	}
	if interpreter != "" {
		paths = append(paths, interpreter)
	}
	paths = append(paths, policy.SystemExecutables...)
	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 || abi < 1 {
		return errors.New("protected launch requires Landlock to restrict execution")
	}
	attr := unix.LandlockRulesetAttr{Handled_access_fs: unix.LANDLOCK_ACCESS_FS_EXECUTE}
	if abi >= 2 {
		// REFER is implicitly denied from ABI 2, even when omitted here.
		// Permit data reparenting; Landlock still refuses moves that would
		// gain EXECUTE at the destination.
		attr.Handled_access_fs |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	// #nosec G103 -- the kernel copies this fixed-layout attribute during the synchronous syscall.
	ruleset, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Offsetof(attr.Handled_access_net), 0)
	if errno != 0 {
		return fmt.Errorf("execution ruleset: %w", errno)
	}
	defer func() { _ = unix.Close(int(ruleset)) }()
	if abi >= 2 {
		if err := allowExecutionRule(int(ruleset), "/", unix.LANDLOCK_ACCESS_FS_REFER); err != nil {
			return err
		}
	}
	for _, path := range paths {
		if err := allowExecutionRule(int(ruleset), path, unix.LANDLOCK_ACCESS_FS_EXECUTE); err != nil {
			return err
		}
	}
	if _, _, errno := syscall.AllThreadsSyscall(unix.SYS_LANDLOCK_RESTRICT_SELF, ruleset, 0, 0); errno != 0 {
		return fmt.Errorf("restrict execution: %w", errno)
	}
	return nil
}

// allowExecutionRule grants one right in the execution ruleset below a
// root-controlled path. Reparenting and execution have separate grants.
func allowExecutionRule(ruleset int, path string, access uint64) error {
	object, err := openRootControlled(path, true)
	if errors.Is(err, unix.ENOTDIR) {
		object, err = openRootControlled(path, false)
	}
	if err != nil {
		return fmt.Errorf("executable path %s: %w", path, err)
	}
	defer func() { _ = object.Close() }()
	if object.Fd() > 1<<31-1 {
		return errors.New("executable path descriptor exceeds the Landlock ABI")
	}
	rule := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(object.Fd())}
	// #nosec G103 -- the kernel copies the fixed-layout rule before this synchronous syscall returns.
	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&rule)), 0, 0, 0); errno != 0 {
		return fmt.Errorf("execution ruleset path %s: %w", path, errno)
	}
	return nil
}

// programInterpreter is the ELF interpreter the kernel opens to execute the
// image, or empty for a static image.
func programInterpreter(image *os.File) (string, error) {
	file, err := elf.NewFile(image)
	if err != nil {
		return "", fmt.Errorf("desktop image: %w", err)
	}
	for _, program := range file.Progs {
		if program.Type != elf.PT_INTERP {
			continue
		}
		raw := make([]byte, program.Filesz)
		if _, err := program.ReadAt(raw, 0); err != nil {
			return "", err
		}
		path := strings.TrimRight(string(raw), "\x00")
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return "", errors.New("desktop image names an unclean program interpreter")
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
		return resolved, nil
	}
	return "", nil
}
