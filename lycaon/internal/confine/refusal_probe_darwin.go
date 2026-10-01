//go:build darwin && cgo

package confine

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// Kernel refusal reporting starts after exec.
const settleProbeTarget = "/usr/bin/touch"

// probe triggers a tagged denial after exec to mark the stream position.
func (logStreamSource) probe(ctx context.Context, tag string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	profile := "(version 1)\n" +
		"(deny default (with message " + sbplString(refusalTagPrefix+tag) + "))\n" +
		"(allow process-exec*)\n(allow file-read*)\n(allow sysctl-read)\n" +
		"(allow file-write-data (literal \"/dev/dtracehelper\"))\n"
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	target := filepath.Join(os.TempDir(), "pwc1-settle-"+tag)
	cmd := exec.CommandContext(ctx, self, helperFlag, profileFDFlag, strconv.Itoa(helperProfileFD), "--", settleProbeTarget, target)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.ExtraFiles = []*os.File{pr}
	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = io.WriteString(pw, profile)
	}()
	err = cmd.Run()
	_ = pr.Close()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// A denied write exits nonzero; its kernel report confirms the barrier.
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() != HelperFailureExit && exit.ExitCode() != CommandNotFoundExit {
		return nil
	}
	if err == nil {
		_ = os.Remove(target)
		return errors.New("settle probe write was not refused")
	}
	return err
}
