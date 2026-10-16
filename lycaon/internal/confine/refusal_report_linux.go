//go:build linux

package confine

import (
	"bufio"
	"encoding/json"
	"os"
	"syscall"

	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
)

// supervisedRefusalReports is true on Linux: no system stream exists, and
// each launch's supervising helper reports the socket calls it refused on a
// pipe of its own.
func supervisedRefusalReports() bool { return true }

func newRefusalSource(string) refusalSource { return nil }

// refusalReport opens the pipe a launch's supervising helper reports on and
// reads it into the action's feed until the helper exits. The returned
// function closes the host's copy of the write end once the helper starts.
func refusalReport(c Confinement) (*os.File, func()) {
	feed := refusalWatch.feedFor(c.refusalTag)
	if feed == nil {
		return nil, func() {}
	}
	read, write, err := os.Pipe()
	if err != nil {
		feed.degrade(WitnessIncomplete)
		return nil, func() {}
	}
	feed.reports.Add(1)
	go func() {
		defer feed.reports.Done()
		defer func() { _ = read.Close() }()
		scanner := bufio.NewScanner(read)
		for scanner.Scan() {
			var r linuxsandbox.Refusal
			if json.Unmarshal(scanner.Bytes(), &r) != nil || r.Unexamined || r.Operation == "" {
				feed.degrade(WitnessIncomplete)
				continue
			}
			feed.record(kernelRefusal{Process: r.Process, Operation: r.Operation, Target: r.Target, Count: 1})
		}
		if scanner.Err() != nil {
			feed.degrade(WitnessIncomplete)
		}
	}()
	return write, func() { _ = write.Close() }
}

// helperRefusalFile is the refusal pipe this helper was handed, if any. It
// stays with the supervising helper: no sandboxed process inherits it.
func helperRefusalFile() *os.File {
	if helperRefusalFDArg < 0 {
		return nil
	}
	syscall.CloseOnExec(helperRefusalFDArg)
	return os.NewFile(uintptr(helperRefusalFDArg), "refusal-report")
}
