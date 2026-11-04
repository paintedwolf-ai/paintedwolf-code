//go:build linux

package confine

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
)

// The plan cannot refuse one loopback port, so a boundary naming one fails closed rather than
// running without it.
func TestLinuxPlanRefusesADeniedLoopbackPort(t *testing.T) {
	_, err := renderProfile(Confinement{Roots: []string{t.TempDir()}, View: ViewBoundary{DeniedLoopbackPorts: []uint16{2049}}})
	var unsupported *linuxsandbox.Unsupported
	if !errors.As(err, &unsupported) || unsupported.Code != linuxsandbox.UnsupportedNetworkGrant {
		t.Fatalf("err = %v", err)
	}
}

func TestLinuxPlanHidesViewStorage(t *testing.T) {
	root, storage := t.TempDir(), t.TempDir()
	rules, err := filesystemRules(Confinement{Roots: []string{root}, View: ViewBoundary{DeniedPaths: []string{storage}}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := compileFilesystemPlan(rules)
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range plan.Hidden {
		if hidden == resolveEach([]string{storage})[0] {
			return
		}
	}
	t.Fatalf("storage is not hidden: %v", plan.Hidden)
}

// A denied tree inside a denied tree has nothing to cover, unless a restored path brought it back.
func TestLinuxPlanMasksANestedDenialOnlyWhereItExists(t *testing.T) {
	pc := &planCompiler{
		readOnly: map[string]bool{},
		hidden:   map[string]bool{"/state": true, "/state/branches": true, "/state/back/storage": true},
		restored: map[string]bool{"/state/back": true},
	}
	for path, want := range map[string]bool{"/state": false, "/state/branches": true, "/state/back/storage": false} {
		if got := pc.insidePlaceholder(path); got != want {
			t.Fatalf("insidePlaceholder(%s) = %v", path, got)
		}
	}
}
