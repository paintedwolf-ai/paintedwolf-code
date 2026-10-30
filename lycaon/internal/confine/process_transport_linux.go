//go:build linux

package confine

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
	"github.com/lycaon/lycaon/internal/lineage"
	"golang.org/x/sys/unix"
)

func pinnedNamespaceTransport(args []string) ([]string, []*os.File, error) {
	if helperPinnedReadyFDArg < 0 {
		return args, nil, nil
	}
	source := []int{helperPinnedCwdFDArg, helperPinnedReadyFDArg, helperPinnedReleaseFDArg, helperPinnedExecutableFDArg}
	files := make([]*os.File, 0, len(source))
	closeFiles := func() {
		for _, file := range files {
			_ = file.Close()
		}
	}
	for _, fd := range source {
		if fd < 0 {
			closeFiles()
			return nil, nil, fmt.Errorf("incomplete native launch namespace transport")
		}
		copied, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			closeFiles()
			return nil, nil, err
		}
		files = append(files, os.NewFile(uintptr(copied), "native-launch-input"))
	}
	out := append([]string(nil), args...)
	first := linuxsandbox.InheritedStartFD
	destinations := map[string]int{pinCwdFDFlag: first, pinReadyFDFlag: first + 1, pinReleaseFDFlag: first + 2, pinExecutableFDFlag: first + 3}
	for i := 0; i < len(out) && out[i] != "--"; i++ {
		if fd, ok := destinations[out[i]]; ok {
			if i+1 >= len(out) {
				closeFiles()
				return nil, nil, fmt.Errorf("missing native launch namespace operand")
			}
			out[i+1] = strconv.Itoa(fd)
			i++
		}
	}
	closePinnedHelperInputs()
	return out, files, nil
}

func pinnedFinalTarget(self string, target []string) (string, []string, []uintptr, error) {
	if helperPinnedExecutableFDArg < 0 || helperPinnedCwdFDArg < 0 || helperPinnedReadyFDArg < 0 || helperPinnedReleaseFDArg < 0 {
		return "", nil, nil, fmt.Errorf("incomplete final native launch descriptor transport")
	}
	files := []uintptr{0, 1, 2, uintptr(helperPinnedCwdFDArg), uintptr(helperPinnedReadyFDArg), uintptr(helperPinnedReleaseFDArg), uintptr(helperPinnedExecutableFDArg)}
	entry := helperPinnedEntryArg
	if len(target) > 0 && strings.ContainsRune(target[0], '/') {
		entry = target[0]
	}
	args := []string{self, helperFlag, pinFinalFlag, pinEntryFlag, entry, pinCwdFDFlag, "3", pinReadyFDFlag, "4", pinReleaseFDFlag, "5", pinExecutableFDFlag, "6"}
	if _, err := unix.FcntlInt(uintptr(lineage.ChildFD), unix.F_GETFD, 0); err == nil {
		files = append(files, uintptr(lineage.ChildFD))
		args = append(args, lineageFDFlag, "7")
	}
	return self, append(append(args, "--"), target...), files, nil
}

func closePinnedHelperInputs() {
	for _, fd := range []int{helperPinnedCwdFDArg, helperPinnedReadyFDArg, helperPinnedReleaseFDArg, helperPinnedExecutableFDArg} {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
	}
	helperPinnedCwdFDArg, helperPinnedReadyFDArg, helperPinnedReleaseFDArg, helperPinnedExecutableFDArg = -1, -1, -1, -1
}
