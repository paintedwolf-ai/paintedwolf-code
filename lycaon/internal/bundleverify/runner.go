package bundleverify

import (
	"bytes"
	"context"
	"strconv"
	"strings"

	execpkg "github.com/lycaon/lycaon/internal/exec"
)

// Runner executes a bundle inspection tool.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd, cleanup, err := execpkg.PrepareCommand(ctx, name, args, execpkg.ExecOpts{
		Launch: execpkg.HostLaunch("bundle_verification"),
	})
	if err != nil {
		return nil, nil, err
	}
	defer cleanup()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// hardenedRuntimeFlag is the CS_RUNTIME bit in codesign's flags word.
const hardenedRuntimeFlag = 0x10000

// adhocFlag is the CS_ADHOC bit in codesign's flags word.
const adhocFlag = 0x2

// parseCodesignFlags reads the documented hexadecimal flags token.
func parseCodesignFlags(out []byte) (uint32, bool) {
	const token = "flags=0x"

	idx := strings.Index(string(out), token)
	if idx < 0 {
		return 0, false
	}
	rest := string(out)[idx+len(token):]

	end := strings.IndexFunc(rest, func(r rune) bool {
		return !isHexDigit(r)
	})
	if end == 0 {
		return 0, false
	}
	if end < 0 {
		end = len(rest)
	}

	v, err := strconv.ParseUint(rest[:end], 16, 32)
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}

func isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// isAdhocSignature accepts either structured signature marker.
func isAdhocSignature(out []byte, flags uint32, haveFlags bool) bool {
	if haveFlags && flags&adhocFlag != 0 {
		return true
	}
	return strings.Contains(string(out), "Signature=adhoc")
}
