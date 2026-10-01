package settings_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestContainedFSBlastAutoApproves(t *testing.T) {
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "load approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	proj := t.TempDir()
	contained := hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{proj}}

	for _, cmd := range []string{
		"rm -rf ./build",
		"dd if=/dev/zero of=out.bin bs=1 count=1",
		"rm --force ./a.o",
		"git push origin main",
		"curl -X POST https://example.com",
		"ssh deploy@host",
	} {
		res, err := gate.Evaluate(context.Background(), hitl.ProposedAction{
			Tool: "command", Args: map[string]any{"command": cmd}, ProjectDir: proj, Contained: contained,
		})
		testutil.FailErr(t, "evaluate Contained", err)
		if !res.AutoApproved() || res.Required() {
			t.Fatalf("Contained %q must auto-approve (FS/egress boundary): %+v", cmd, res)
		}
	}

	escape, err := gate.Evaluate(context.Background(), hitl.ProposedAction{
		Tool: "command", Args: map[string]any{"command": "rm -rf /etc/nginx"}, ProjectDir: proj, Contained: contained,
	})
	testutil.FailErr(t, "evaluate contained command", err)
	if !escape.AutoApproved() || escape.Required() {
		t.Fatalf("contained command must not be classified from argv text: %+v", escape)
	}

	sudo, err := gate.Evaluate(context.Background(), hitl.ProposedAction{
		Tool: "command", Args: map[string]any{"command": "sudo id"}, ProjectDir: proj, Contained: contained,
	})
	testutil.FailErr(t, "evaluate sudo", err)
	if !sudo.AutoApproved() || sudo.Required() {
		t.Fatalf("host-side effect auto-approves when Contained (confinement contains the effect): %+v", sudo)
	}
}
