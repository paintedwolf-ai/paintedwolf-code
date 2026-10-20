//go:build darwin || linux

package confine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
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
	return os.Open(path)
}
func (p *pinPathFixture) OpenCwd(ctx context.Context) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.refused {
		return nil, os.ErrPermission
	}
	return os.Open(p.cwd)
}

func TestLaunchPinsKeepSelectedEntryCwdAndCurrentAdmissionDistinct(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "fixture-script")
	if err := os.WriteFile(entry, []byte("#!/bin/sh -e\nprintf synthetic\n"), 0700); err != nil {
		t.Fatal(err)
	}
	paths := &pinPathFixture{cwd: root, allowed: map[string]bool{entry: true, "/bin/sh": true}}
	pins, err := CaptureProcessPins(t.Context(), entry, paths, pinFixtureLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer pins.Close()
	if len(pins.images) != 2 || len(pins.words) != 1 || pins.words[0][0] != "/bin/sh" {
		t.Fatal("interpreter selection was not captured")
	}
	if err := pins.revalidate(t.Context()); err != nil {
		t.Fatal(err)
	}
	paths.refused = true
	if err := pins.revalidate(t.Context()); !errors.Is(err, os.ErrPermission) {
		t.Fatal("held descriptors bypassed current authority")
	}
	paths.refused = false
	if err := os.Rename(entry, entry+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte("#!/bin/sh -e\nprintf replacement\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := pins.revalidate(t.Context()); !errors.Is(err, ErrLaunchIdentityChanged) {
		t.Fatal("replaced selected entry was accepted")
	}
	if err := pins.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pins.revalidate(t.Context()); !errors.Is(err, ErrLaunchIdentityChanged) {
		t.Fatal("closed launch pins reopened")
	}
}

func TestLaunchPinsRejectChangedCwdAndCancelledCapture(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "cwd")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(root, "fixture-script")
	if err := os.WriteFile(entry, []byte("#!/bin/sh\nprintf synthetic\n"), 0700); err != nil {
		t.Fatal(err)
	}
	paths := &pinPathFixture{cwd: cwd, allowed: map[string]bool{entry: true, "/bin/sh": true}}
	pins, err := CaptureProcessPins(t.Context(), entry, paths, pinFixtureLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer pins.Close()
	if err := os.Rename(cwd, cwd+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	if err := pins.revalidate(t.Context()); !errors.Is(err, ErrLaunchIdentityChanged) {
		t.Fatal("replacement cwd was accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if capture, err := CaptureProcessPins(ctx, entry, paths, pinFixtureLimits()); capture != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled capture was admitted")
	}
}

func pinFixtureLimits() LaunchVectorLimits {
	return LaunchVectorLimits{ArgumentBytes: 128 << 10, ArgvBytes: 1 << 20, EnvironmentBytes: 1 << 20, ExpandedFields: 16384}
}

func TestLaunchVectorLimitsRefuseBeforeCopyAndPreserveLiteralData(t *testing.T) {
	limits := LaunchVectorLimits{ArgumentBytes: 4, ArgvBytes: 6, EnvironmentBytes: 4, ExpandedFields: 2}
	if err := limits.checkCommand("abcd", []string{"ef"}, []string{"K=xy"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args, environment []string
		dimension         string
	}{
		{[]string{"abcde"}, nil, "argument_bytes"},
		{[]string{"abcd", "efg"}, nil, "argv_bytes"},
		{[]string{"a", "b", "c"}, nil, "expanded_fields"},
		{[]string{"a"}, []string{"K=xyz"}, "environment_bytes"},
	}
	for _, c := range cases {
		var failure *LaunchVectorLimitError
		if err := limits.checkCommand(c.args[0], c.args[1:], c.environment); !errors.As(err, &failure) || failure.Dimension != c.dimension {
			t.Fatalf("wrong typed vector refusal for %s", c.dimension)
		}
	}
	if err := limits.checkCommand("a\x00b", nil, nil); err == nil {
		t.Fatal("NUL scalar admitted")
	}
	if err := (LaunchVectorLimits{}).checkCommand("a", nil, nil); err == nil {
		t.Fatal("unbounded launch vectors admitted")
	}
}
