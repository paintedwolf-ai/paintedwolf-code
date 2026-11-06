//go:build unix

package userpath

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

const (
	detachedProbeHelperFDEnv     = "USERPATH_DETACHED_FD"
	detachedProbeHelperReportEnv = "USERPATH_DETACHED_REPORT"
)

func TestProbeDoesNotLeakInheritedDescriptorsToDetachedChildren(t *testing.T) {
	readEnd, writeEnd, err := os.Pipe()
	testutil.FailErr(t, "create pipe", err)
	defer func() { _ = readEnd.Close() }()

	// F_DUPFD duplicates the descriptor with FD_CLOEXEC cleared, matching a
	// supervisor lock descriptor inherited from a parent shell.
	inheritedFD, err := unix.FcntlInt(writeEnd.Fd(), unix.F_DUPFD, 200)
	testutil.FailErr(t, "duplicate non-cloexec descriptor", err)
	testutil.FailErr(t, "close original pipe writer", writeEnd.Close())
	inheritedClosed := false
	defer func() {
		if !inheritedClosed {
			_ = unix.Close(inheritedFD)
		}
	}()

	flags, err := unix.FcntlInt(uintptr(inheritedFD), unix.F_GETFD, 0)
	testutil.FailErr(t, "read initial descriptor flags", err)
	if flags&unix.FD_CLOEXEC != 0 {
		t.Fatalf("descriptor %d started with FD_CLOEXEC set (%#x)", inheritedFD, flags)
	}

	reportPath := filepath.Join(t.TempDir(), "detached.report")
	cfg := testConfig()
	cfg.Probe.Timeout = 5 * time.Second
	cfg.Probe.EnvAllowlist = append(cfg.Probe.EnvAllowlist, detachedProbeHelperFDEnv, detachedProbeHelperReportEnv)
	cfg.Shells = []ShellInvocation{{
		Names: []string{"sh"},
		Args:  []string{"-c"},
		Command: strconv.Quote(os.Args[0]) + ` -test.run=^TestProbeDetachedChildHelper$ </dev/null >/dev/null 2>&1 & ` +
			`while [ ! -s ` + strconv.Quote(reportPath) + ` ]; do /bin/sleep 0.01; done; ` +
			`printf "%s\n%s\n%s\n" "{marker}" "/opt/toolchain/bin:/usr/bin" "{marker}"`,
	}}

	p := providerWith(t, cfg, "/bin/sh", map[string]string{
		"SHELL":                      "/bin/sh",
		"PATH":                       "/usr/bin:/bin",
		detachedProbeHelperFDEnv:     strconv.Itoa(inheritedFD),
		detachedProbeHelperReportEnv: reportPath,
	}, fakeShell{})
	p.prepare = execpkg.PrepareCommand
	p.run = defaultRun

	snap := p.Resolve(context.Background())
	if snap.Source() != SourceProbe || snap.Value() != "/opt/toolchain/bin:/usr/bin" {
		t.Fatalf("source/path = %q/%q (%s), want the login shell answer", snap.Source(), snap.Value(), snap.Reason())
	}

	flags, err = unix.FcntlInt(uintptr(inheritedFD), unix.F_GETFD, 0)
	testutil.FailErr(t, "read descriptor flags after probe", err)
	if flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("descriptor %d still lacks FD_CLOEXEC after probe (%#x)", inheritedFD, flags)
	}

	raw, err := os.ReadFile(reportPath)
	testutil.FailErr(t, "read detached child report", err)
	_, status, ok := strings.Cut(strings.TrimSpace(string(raw)), ":")
	if !ok {
		t.Fatalf("malformed detached child report %q", raw)
	}
	if status != "closed" {
		t.Fatalf("detached child observed descriptor %d as %q, want closed", inheritedFD, status)
	}

	testutil.FailErr(t, "close parent descriptor", unix.Close(inheritedFD))
	inheritedClosed = true

	eofDone := make(chan error, 1)
	go func() {
		var buf [1]byte
		_, err := readEnd.Read(buf[:])
		eofDone <- err
	}()
	select {
	case err := <-eofDone:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("pipe read after closing parent descriptor = %v, want EOF", err)
		}
	case <-time.After(testutil.Timeout(time.Minute)):
		t.Fatalf("pipe writer remained open in a descendant after parent closed descriptor %d", inheritedFD)
	}
}

func TestProbeDetachedChildHelper(t *testing.T) {
	fdText := os.Getenv(detachedProbeHelperFDEnv)
	reportPath := os.Getenv(detachedProbeHelperReportEnv)
	if fdText == "" || reportPath == "" {
		return
	}
	_, _ = unix.Setsid()
	fd, err := strconv.Atoi(fdText)
	if err != nil {
		t.Fatalf("parse descriptor %q: %v", fdText, err)
	}
	status := "open"
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); errors.Is(err, syscall.EBADF) {
		status = "closed"
	}
	report := fmt.Sprintf("%d:%s\n", os.Getpid(), status)
	testutil.FailErr(t, "write detached report", os.WriteFile(reportPath, []byte(report), 0o600))
}

func TestCloseOnExecFallbackMarksRangeWhenFDDirectoryUnavailable(t *testing.T) {
	readEnd, writeEnd, err := os.Pipe()
	testutil.FailErr(t, "create pipe", err)
	defer func() {
		_ = readEnd.Close()
		_ = writeEnd.Close()
	}()

	dupFD, err := unix.FcntlInt(writeEnd.Fd(), unix.F_DUPFD, 16)
	testutil.FailErr(t, "duplicate non-cloexec descriptor", err)
	defer func() { _ = unix.Close(dupFD) }()

	closeOnExecIn(filepath.Join(t.TempDir(), "missing-fd-dir"), dupFD+1)

	flags, err := unix.FcntlInt(uintptr(dupFD), unix.F_GETFD, 0)
	testutil.FailErr(t, "read fallback descriptor flags", err)
	if flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("fallback range left descriptor %d without FD_CLOEXEC (%#x)", dupFD, flags)
	}
}
