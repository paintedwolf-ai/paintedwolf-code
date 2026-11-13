//go:build linux || darwin

package gitcredential

import (
	"errors"
	"os"

	"github.com/lycaon/lycaon/internal/osprocess"
)

// maxAncestry bounds the walk from the helper to the served git process.
const maxAncestry = 16

// descendsFromServedGit walks up from the peer to the served process and
// requires a git process on the way, the served process included.
func descendsFromServedGit(peer int, gitBinary string, served servedProcess) error {
	sawGit := false
	pid, err := parentProcess(peer)
	for hops := 0; err == nil && pid > 1 && hops < maxAncestry; hops++ {
		if exe, exeErr := processExecutable(pid); exeErr == nil && sameFile(exe, gitBinary) {
			sawGit = true
		}
		if pid == served.pid {
			start, ok := osprocess.StartTime(pid)
			if ok && start == served.start && sawGit {
				return nil
			}
			break
		}
		pid, err = parentProcess(pid)
	}
	return errors.New("credential helper peer does not descend from the served git process")
}

func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}
