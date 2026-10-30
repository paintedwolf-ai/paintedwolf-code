//go:build darwin || linux

package confine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type pinPathFixture struct {
	cwd     string
	allowed map[string]bool
	refused bool
}

func (p *pinPathFixture) OpenExecutable(ctx context.Context, path string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.refused || !p.allowed[path] {
		return nil, os.ErrPermission
	}
	return openNativeLaunchMetadata(path, false)
}
func (p *pinPathFixture) OpenCwd(ctx context.Context) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.refused {
		return nil, os.ErrPermission
	}
	return openNativeLaunchMetadata(p.cwd, true)
}
func pinFixtureLimits() LaunchVectorLimits {
	return LaunchVectorLimits{ArgumentBytes: 128 << 10, ArgvBytes: 1 << 20, EnvironmentBytes: 1 << 20, ExpandedFields: 16384}
}
func TestLaunchInputsKeepSelectionCwdAndCurrentAuthorityDistinct(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "fixture-script")
	if err := os.WriteFile(entry, []byte("#!/bin/sh\nprintf synthetic\n"), 0100); err != nil {
		testutil.FailErr(t, "write selected executable fixture", err)
	}
	paths := &pinPathFixture{cwd: root, allowed: map[string]bool{entry: true}}
	inputs, err := CaptureProcessInputs(t.Context(), entry, paths, pinFixtureLimits())
	if err != nil {
		testutil.FailErr(t, "capture admitted process inputs", err)
	}
	defer inputs.Close()
	if inputs.entry.path != entry {
		t.Fatal("selected entry changed")
	}
	if err := inputs.revalidate(t.Context()); err != nil {
		testutil.FailErr(t, "revalidate unchanged process inputs", err)
	}
	paths.refused = true
	if err := inputs.revalidate(t.Context()); !errors.Is(err, os.ErrPermission) {
		t.Fatal("held metadata waived current authority")
	}
	paths.refused = false
	if err := os.Rename(entry, entry+".old"); err != nil {
		testutil.FailErr(t, "retain selected executable before replacement", err)
	}
	if err := os.WriteFile(entry, []byte("#!/bin/sh\nprintf replacement\n"), 0700); err != nil {
		testutil.FailErr(t, "write replacement executable", err)
	}
	if err := inputs.revalidate(t.Context()); !errors.Is(err, ErrLaunchIdentityChanged) {
		t.Fatal("replaced selected entry accepted")
	}
}
func TestLaunchInputsRejectReplacedCwdClosedInputsAndCancelledCapture(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "cwd")
	if err := os.Mkdir(cwd, 0700); err != nil {
		testutil.FailErr(t, "create selected working directory", err)
	}
	entry := filepath.Join(root, "entry")
	if err := os.WriteFile(entry, []byte("native fixture"), 0100); err != nil {
		testutil.FailErr(t, "write working-directory executable", err)
	}
	paths := &pinPathFixture{cwd: cwd, allowed: map[string]bool{entry: true}}
	inputs, err := CaptureProcessInputs(t.Context(), entry, paths, pinFixtureLimits())
	if err != nil {
		testutil.FailErr(t, "capture working-directory inputs", err)
	}
	defer inputs.Close()
	if err := os.Rename(cwd, cwd+".old"); err != nil {
		testutil.FailErr(t, "retain original working directory", err)
	}
	if err := os.Mkdir(cwd, 0700); err != nil {
		testutil.FailErr(t, "create replacement working directory", err)
	}
	if err := inputs.revalidate(t.Context()); !errors.Is(err, ErrLaunchIdentityChanged) {
		t.Fatal("replaced cwd accepted")
	}
	if err := inputs.Close(); err != nil {
		testutil.FailErr(t, "close captured process inputs", err)
	}
	if err := inputs.revalidate(t.Context()); !errors.Is(err, ErrLaunchIdentityChanged) {
		t.Fatal("closed input reopened")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if inputs, err := CaptureProcessInputs(ctx, entry, paths, pinFixtureLimits()); inputs != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled capture accepted")
	}
}
func TestLaunchVectorLimitsRefuseBeforeCopyAndPreserveLiteralData(t *testing.T) {
	limits := LaunchVectorLimits{ArgumentBytes: 4, ArgvBytes: 6, EnvironmentBytes: 4, ExpandedFields: 2}
	if err := limits.checkCommand("abcd", []string{"ef"}, []string{"K=xy"}); err != nil {
		testutil.FailErr(t, "validate exact launch vector limits", err)
	}
	for _, c := range []struct {
		args, env []string
		dimension string
	}{{[]string{"abcde"}, nil, "argument_bytes"}, {[]string{"abcd", "efg"}, nil, "argv_bytes"}, {[]string{"a", "b", "c"}, nil, "expanded_fields"}, {[]string{"a"}, []string{"K=xyz"}, "environment_bytes"}} {
		var bound *LaunchVectorLimitError
		if err := limits.checkCommand(c.args[0], c.args[1:], c.env); !errors.As(err, &bound) || bound.Dimension != c.dimension {
			t.Fatal("wrong typed vector refusal")
		}
	}
	if err := limits.checkCommand("a\x00b", nil, nil); err == nil {
		t.Fatal("NUL admitted")
	}
	if err := (LaunchVectorLimits{}).checkCommand("a", nil, nil); err == nil {
		t.Fatal("missing limits admitted")
	}
}
