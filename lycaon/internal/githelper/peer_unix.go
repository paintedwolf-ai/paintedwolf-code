//go:build linux || darwin

package githelper

import (
	"errors"
	"os"

	"github.com/lycaon/lycaon/internal/osprocess"
)

// maxAncestry bounds the parent walk from the helper to the served process.
const maxAncestry = 16

// descendsFromServedGit requires a bundled git process along the ancestry up to served.
func descendsFromServedGit(peer int, gitBinary string, served ServedProcess) error {
	sawGit := false
	pid, err := parentProcess(peer)
	for hops := 0; err == nil && pid > 1 && hops < maxAncestry; hops++ {
		if exe, exeErr := processExecutable(pid); exeErr == nil && sameFile(exe, gitBinary) {
			sawGit = true
		}
		if pid == served.PID {
			start, ok := osprocess.StartTime(pid)
			if ok && start == served.Start && sawGit {
				return nil
			}
			break
		}
		pid, err = parentProcess(pid)
	}
	return errors.New("git helper peer does not descend from the served git process")
}

func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}
