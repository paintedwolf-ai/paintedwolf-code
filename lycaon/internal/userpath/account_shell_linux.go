//go:build linux

package userpath

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	execpkg "github.com/lycaon/lycaon/internal/exec"
)

func defaultAccountShell(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	program, err := linuxAccountLookupPath(os.Stat)
	if err != nil {
		return "", err
	}
	return linuxAccountShell(ctx, program, os.Getuid())
}

func linuxAccountLookupPath(stat func(string) (fs.FileInfo, error)) (string, error) {
	// NSS is host identity plumbing; a project or person PATH cannot replace it.
	for _, program := range []string{"/usr/bin/getent", "/run/current-system/sw/bin/getent"} {
		info, err := stat(program)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("account lookup utility unavailable: %w", err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return "", errors.New("account lookup utility is not a regular executable")
		}
		return program, nil
	}
	return "", errors.New("account lookup utility absent from system locations")
}

// NSS resolves local and directory-backed accounts by uid.
func linuxAccountShell(ctx context.Context, program string, uid int) (string, error) {
	if uid < 0 {
		return "", errors.New("account lookup requires a nonnegative uid")
	}
	cmd, cleanup, err := execpkg.PrepareCommand(ctx, program, []string{"passwd", strconv.Itoa(uid)}, execpkg.ExecOpts{
		Launch: execpkg.HostLaunch("user_account_shell"), Env: []string{"LC_ALL=C", "PATH=/usr/bin:/bin"},
	})
	if err != nil {
		return "", err
	}
	defer cleanup()
	cmd.WaitDelay = probeWaitDelay
	output := boundedBuffer{limit: 64 << 10}
	cmd.Stdout = &output
	if err := defaultRun(cmd); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("account lookup canceled: %w", ctx.Err())
		}
		return "", fmt.Errorf("account lookup failed: %w", err)
	}
	if output.overflow {
		return "", errors.New("account lookup exceeded its capture limit")
	}
	return linuxAccountRecordShell(output.String(), uint64(uid))
}

func linuxAccountRecordShell(raw string, uid uint64) (string, error) {
	record := strings.TrimSuffix(raw, "\n")
	fields := strings.Split(record, ":")
	if len(fields) != 7 || fields[0] == "" || strings.ContainsAny(record, "\n\r\x00") {
		return "", errors.New("account lookup returned an invalid record")
	}
	got, err := strconv.ParseUint(fields[2], 10, 32)
	if err != nil || got != uid {
		return "", errors.New("account lookup returned a different uid")
	}
	if !filepath.IsAbs(fields[6]) {
		return "", errors.New("account lookup returned no absolute shell")
	}
	return fields[6], nil
}
