//go:build darwin || linux

package confine_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStateTreeAncestorRootsRefuseBeforeLaunch(t *testing.T) {
	self, err := os.Executable()
	testutil.FailErr(t, "locate confinement helper", err)
	for _, lane := range []string{"attached", "reviewed", "scratch", "durable"} {
		t.Run(lane, func(t *testing.T) {
			base := t.TempDir()
			parent := filepath.Join(base, "parent")
			state := filepath.Join(parent, "state")
			testutil.FailErr(t, "create isolated state", os.MkdirAll(state, 0700))
			t.Setenv(configdir.EnvConfigDir, state)
			c := confine.Confinement{Roots: []string{t.TempDir()}, Network: confine.NetworkDeny}
			switch lane {
			case "attached":
				c.Roots = []string{base}
			case "reviewed":
				c.GrantedWriteRoots = []string{base}
			case "scratch":
				c.SessionScratchRoot = base
			case "durable":
				confine.SetGrantedWriteRootsSource(func(string) []string { return []string{base} })
				t.Cleanup(func() { confine.SetGrantedWriteRootsSource(nil) })
			}
			moved := filepath.Join(base, "moved")
			cmd, cleanup, launchErr := confine.Command(t.Context(), self, "/bin/mv", []string{parent, moved}, c)
			defer cleanup()
			if launchErr == nil {
				out, runErr := cmd.CombinedOutput()
				_, stateErr := os.Stat(filepath.Join(moved, "state"))
				t.Fatalf("ancestor reached launch: run=%v moved_state=%v output=%s", runErr, stateErr == nil, out)
			}
			var refusal *confine.WriteRootRefusalError
			if !errors.As(launchErr, &refusal) {
				t.Fatalf("launch lost typed root refusal: %v", launchErr)
			}
			if _, err := os.Stat(state); err != nil {
				t.Fatalf("refused launch changed state: %v", err)
			}
		})
	}
}

func TestStateTreeAncestorsExcludedFromDefaultRoots(t *testing.T) {
	for _, source := range []string{"temporary", "cache"} {
		t.Run(source, func(t *testing.T) {
			base := t.TempDir()
			if source == "cache" {
				t.Setenv("XDG_CACHE_HOME", base)
			}
			state := filepath.Join(base, "state")
			t.Setenv(configdir.EnvConfigDir, state)
			roots, err := confine.WriteRootsForBoundary("", []string{t.TempDir()}, nil, "")
			testutil.FailErr(t, "admit roots without unsafe defaults", err)
			if confine.PathWithinWriteRoots(state, roots) {
				t.Fatalf("default root still covers state: %v", roots)
			}
			if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("root admission changed state storage: %v", err)
			}
		})
	}
}

func TestOrdinaryReviewedRootStillLaunches(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv(configdir.EnvConfigDir, state)
	project, granted := t.TempDir(), t.TempDir()
	marker := filepath.Join(granted, "result")
	self, err := os.Executable()
	testutil.FailErr(t, "locate helper", err)
	cmd, cleanup, err := confine.Command(t.Context(), self, "/bin/sh", []string{"-c", "printf approved > \"$1\"", "fixture", marker}, confine.Confinement{Roots: []string{project}, GrantedWriteRoots: []string{granted}, Network: confine.NetworkDeny})
	testutil.FailErr(t, "prepare ordinary reviewed launch", err)
	defer cleanup()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ordinary reviewed launch failed: %v: %s", err, out)
	}
	got, err := os.ReadFile(marker)
	testutil.FailErr(t, "read reviewed effect", err)
	if string(got) != "approved" {
		t.Fatalf("reviewed effect changed: %q", got)
	}
}
