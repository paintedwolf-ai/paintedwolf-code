//go:build linux

// Package hostread owns fixed native capability reads below agent execution.
package hostread

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	execpkg "github.com/lycaon/lycaon/internal/exec"
)

const outputLimit = 64 << 10

// Nix builds bind this reader to their retained GLib tool closure.
var gsettingsExecutable string

var errOutputLimit = errors.New("desktop proxy settings exceed their output limit")

// DesktopProxySettings reads only the GNOME proxy schema through its supported
// reader. This closed operation cannot launch an agent-selected command.
func DesktopProxySettings(ctx context.Context) ([]byte, int, error) {
	if gsettingsExecutable != "" {
		if !filepath.IsAbs(gsettingsExecutable) {
			return nil, -1, errors.New("the packaged desktop settings reader is invalid")
		}
		return read(ctx, gsettingsExecutable)
	}
	for _, program := range []string{"/usr/bin/gsettings", "/bin/gsettings", "/run/current-system/sw/bin/gsettings"} {
		info, err := os.Stat(program)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return read(ctx, program)
		}
	}
	return nil, 1, nil
}

func read(ctx context.Context, program string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, program, "list-recursively", "org.gnome.system.proxy")
	command.Env = append(os.Environ(), "LC_ALL=C")
	command.WaitDelay = 250 * time.Millisecond
	output := &boundedOutput{}
	command.Stdout = output
	command.Stderr = io.Discard
	err := execpkg.RunInOwnGroup(command)
	if ctx.Err() != nil {
		return nil, -1, ctx.Err()
	}
	if output.exceeded {
		return nil, -1, errOutputLimit
	}
	var status *exec.ExitError
	if errors.As(err, &status) {
		return nil, status.ExitCode(), nil
	}
	if err != nil {
		return nil, -1, errors.New("desktop proxy settings reader failed")
	}
	return output.buffer.Bytes(), 0, nil
}

type boundedOutput struct {
	buffer   bytes.Buffer
	exceeded bool
}

func (output *boundedOutput) Write(value []byte) (int, error) {
	if len(value) > outputLimit-output.buffer.Len() {
		output.exceeded = true
		return 0, errOutputLimit
	}
	return output.buffer.Write(value)
}
