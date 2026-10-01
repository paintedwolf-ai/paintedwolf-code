//go:build darwin && cgo

package confine

/*
#include <os/log.h>
#include <stdlib.h>

static void confine_refusal_mark(const char *message) {
    os_log(OS_LOG_DEFAULT, "%{public}s", message);
}
*/
import "C"

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/osprocess"
)

// refusalStreamFile identifies orphaned streams by PID and start time.
const refusalStreamFile = "refusal-watch.json"

// maxStreamDiagnostic bounds the stream's stderr kept for the unavailable reason.
const maxStreamDiagnostic = 512

type recordedStream struct {
	PID   int   `json:"pid"`
	Start int64 `json:"start"`
}

// logStreamSource reads kernel sandbox reports from the unified log.
type logStreamSource struct {
	stateRoot string
}

func newRefusalSource(stateRoot string) refusalSource {
	return logStreamSource{stateRoot: stateRoot}
}

func (s logStreamSource) open(ctx context.Context, selfPID int) (io.ReadCloser, func() error, error) {
	s.reclaim()
	predicate := fmt.Sprintf(
		`(senderImagePath ENDSWITH "/Sandbox" AND (eventMessage CONTAINS %q OR eventMessage ENDSWITH %q)) OR (processID == %d AND eventMessage BEGINSWITH %q)`,
		refusalTagPrefix, truncatedReportSuffix, selfPID, refusalMarkPrefix,
	)
	cmd := exec.CommandContext(ctx, "/usr/bin/log", "stream", "--style", "ndjson", "--predicate", predicate)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{buf: &stderr, max: maxStreamDiagnostic}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	err = cmd.Start()
	if err != nil {
		return nil, nil, err
	}
	s.record(cmd.Process.Pid)
	wait := func() error {
		err := cmd.Wait()
		s.forget(cmd.Process.Pid)
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return errors.Join(err, errors.New(detail))
		}
		return err
	}
	return stdout, wait, nil
}

func (logStreamSource) mark(message string) {
	cMessage := C.CString(message)
	defer C.free(unsafe.Pointer(cMessage))
	C.confine_refusal_mark(cMessage)
}

// reclaim stops a recorded stream only when its PID and start time match.
func (s logStreamSource) reclaim() {
	path := s.path()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var prior recordedStream
	if json.Unmarshal(data, &prior) != nil || prior.PID <= 0 || prior.PID == os.Getpid() {
		return
	}
	if start, ok := osprocess.StartTime(prior.PID); ok && start == prior.Start {
		_ = syscall.Kill(prior.PID, syscall.SIGTERM)
	}
	_ = os.Remove(path)
}

func (s logStreamSource) record(pid int) {
	path := s.path()
	start, ok := osprocess.StartTime(pid)
	if path == "" || !ok {
		return
	}
	data, err := json.Marshal(recordedStream{PID: pid, Start: start})
	if err != nil {
		return
	}
	_, _ = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path),
		Source:   bytes.NewReader(data),
		Mode:     0o600,
		DirMode:  0o700,
	})
}

func (s logStreamSource) forget(pid int) {
	path := s.path()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var recorded recordedStream
	if json.Unmarshal(data, &recorded) == nil && recorded.PID == pid {
		_ = os.Remove(path)
	}
}

func (s logStreamSource) path() string {
	if strings.TrimSpace(s.stateRoot) == "" {
		return ""
	}
	return filepath.Join(s.stateRoot, refusalStreamFile)
}

// limitedWriter keeps the first max bytes written to it.
type limitedWriter struct {
	buf *bytes.Buffer
	max int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		w.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
