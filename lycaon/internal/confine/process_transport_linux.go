//go:build linux

package confine

import (
	"fmt"
	"os"
	"strconv"
	"syscall"

	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
	"github.com/lycaon/lycaon/internal/lineage"
	"golang.org/x/sys/unix"
)

// Namespace relay slots3..6 already carry profile, lineage, signal and control.
// Explicit copies preserve only the held launch descriptors in slots7..9.
func pinnedNamespaceTransport(args []string) ([]string, []*os.File, error) {
	if helperPinnedReadyFDArg < 0 {
		return args, nil, nil
	}
	source := []int{helperPinnedCwdFDArg, helperPinnedReadyFDArg, helperPinnedExecutableFDArg}
	files := make([]*os.File, 0, len(source))
	for _, fd := range source {
		if fd < 0 {
			for _, file := range files {
				_ = file.Close()
			}
			return nil, nil, fmt.Errorf("incomplete pinned namespace transport")
		}
		copied, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			for _, file := range files {
				_ = file.Close()
			}
			return nil, nil, err
		}
		files = append(files, os.NewFile(uintptr(copied), "pinned-namespace-input"))
	}
	out := append([]string(nil), args...)
	destinations := map[string]int{pinCwdFDFlag: linuxsandbox.InheritedStartFD, pinReadyFDFlag: linuxsandbox.InheritedStartFD + 1, pinExecutableFDFlag: linuxsandbox.InheritedStartFD + 2}
	for i := 0; i < len(out) && out[i] != "--"; i++ {
		if fd, ok := destinations[out[i]]; ok {
			if i+1 >= len(out) {
				for _, file := range files {
					_ = file.Close()
				}
				return nil, nil, fmt.Errorf("missing pinned namespace operand")
			}
			out[i+1] = strconv.Itoa(fd)
			i++
		}
	}
	// Only relay copies keep the control peer alive from here. Closing these
	// originals also prevents successful exec EOF being held open by the outer helper.
	for _, fd := range source {
		_ = syscall.Close(fd)
	}
	helperPinnedCwdFDArg, helperPinnedReadyFDArg, helperPinnedExecutableFDArg = -1, -1, -1
	return out, files, nil
}

func pinnedFinalTarget(self string, target []string) (string, []string, []uintptr, error) {
	if helperPinnedExecutableFDArg < 0 || helperPinnedCwdFDArg < 0 || helperPinnedReadyFDArg < 0 {
		return "", nil, nil, fmt.Errorf("incomplete final launch descriptor transport")
	}
	files := []uintptr{0, 1, 2, uintptr(helperPinnedExecutableFDArg), uintptr(helperPinnedCwdFDArg), uintptr(helperPinnedReadyFDArg)}
	args := []string{self, helperFlag, pinFinalFlag, pinEntryFlag, helperPinnedEntryArg, pinExecutableFDFlag, "3", pinCwdFDFlag, "4", pinReadyFDFlag, "5"}
	if _, err := unix.FcntlInt(uintptr(lineage.ChildFD), unix.F_GETFD, 0); err == nil {
		files = append(files, uintptr(lineage.ChildFD))
		args = append(args, lineageFDFlag, "6")
	}
	args = append(append(args, "--"), target...)
	return self, args, files, nil
}

func closePinnedHelperInputs() {
	for _, fd := range []int{helperPinnedCwdFDArg, helperPinnedReadyFDArg, helperPinnedExecutableFDArg} {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
	}
	helperPinnedCwdFDArg, helperPinnedReadyFDArg, helperPinnedExecutableFDArg = -1, -1, -1
}
