//go:build linux

package launchguard

import (
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	ruleset, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Offsetof(attr.Handled_access_net), 0)
	if errno != 0 {
		return fmt.Errorf("execution ruleset: %w", errno)
	}
	defer func() { _ = unix.Close(int(ruleset)) }()
	for _, path := range paths {
		if err := allowExecution(int(ruleset), path); err != nil {
			return err
		}
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, ruleset, 0, 0); errno != 0 {
		return fmt.Errorf("restrict execution: %w", errno)
	}
	return nil
}

// allowExecution adds one root-controlled path the namespace may execute from.
func allowExecution(ruleset int, path string) error {
	directory := true
	info, err := os.Lstat(path)
	if err == nil {
		directory = info.IsDir()
	}
	object, err := openRootControlled(path, directory)
	if err != nil {
		return fmt.Errorf("executable path %s: %w", path, err)
	}
	defer func() { _ = object.Close() }()
	rule := unix.LandlockPathBeneathAttr{Allowed_access: unix.LANDLOCK_ACCESS_FS_EXECUTE, Parent_fd: int32(object.Fd())}
	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&rule)), 0, 0, 0); errno != 0 {
		return fmt.Errorf("allow execution from %s: %w", path, errno)
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
