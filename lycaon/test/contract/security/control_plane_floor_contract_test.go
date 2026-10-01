package contract

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// Verifies that the approval gate and path resolver agree on control plane boundaries.
func TestControlPlaneFloorGateAndResolverAgree(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	proj := t.TempDir()
	approvalGate := approvalGateForFloor(t)
	scratch := enginepaths.SessionScratchUnder(cfg, "chat-floor")
	otherScratch := enginepaths.SessionScratchUnder(cfg, "chat-other")
	for _, dir := range []string{scratch, otherScratch} {
		testutil.FailErr(t, "create session scratch", os.MkdirAll(dir, 0o700))
	}
	tctx := tools.ToolContext{
		Roots:             []projectroot.RootRef{{ID: "primary", Path: proj, IsPrimary: true}},
		ActiveRootID:      "primary",
		SessionID:         "chat-floor",
		SessionScratchDir: scratch,
	}

	cases := []struct {
		name  string
		tool  string
		path  string
		write bool
		deny  bool
	}{
		{"list sessions", "list_dir", filepath.Join(cfg, "debug", "sessions"), false, true},
		{"read token", "read", filepath.Join(cfg, "api.token"), false, true},
		{"grep store", "grep", filepath.Join(cfg, "store.db"), false, true},
		{"write skill", "write", filepath.Join(cfg, "packs", "stock", "skills", "verify-a-change", "SKILL.md"), true, true},
		{"delete session log", "delete", filepath.Join(cfg, "debug", "sessions", "x", "sidecar.log"), true, true},
		{"read draft", "read", filepath.Join(enginepaths.DraftsRootUnder(cfg), "p", "notes.md"), false, false},
		{"write own scratch", "write", filepath.Join(scratch, "flow.sh"), true, false},
		{"read own scratch", "read", filepath.Join(scratch, "flow.sh"), false, false},
		{"write another session's scratch", "write", filepath.Join(otherScratch, "flow.sh"), true, true},
		{"read another session's scratch", "read", filepath.Join(otherScratch, "flow.sh"), false, true},
		{"read elsewhere", "read", filepath.Join(t.TempDir(), "notes.md"), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
				Tool: tc.tool, Files: []string{tc.path}, ProjectDir: proj,
				SessionScratchRoot: scratch,
			})
			testutil.FailErr(t, "gate.Evaluate", err)

			var resolveErr error
			if tc.write {
				_, resolveErr = projectpaths.ResolveWrite(context.Background(), nil, tctx, tc.path)
			} else {
				_, resolveErr = projectpaths.ResolveRead(context.Background(), nil, tctx, tc.path)
			}
			var reject *tools.ToolReject
			resolverCode := ""
			if asToolReject(resolveErr, &reject) {
				resolverCode = reject.Code
			}

			if tc.deny {
				if !res.Denied || res.DenyCode != isolation.CodeControlPlaneDenied || res.Required() {
					t.Fatalf("gate must deny the control plane without a card, got %+v", res)
				}
				if resolverCode != isolation.CodeControlPlaneDenied {
					t.Fatalf("resolver code %q disagrees with gate code %q", resolverCode, res.DenyCode)
				}
				return
			}
			if res.Denied && res.DenyCode == isolation.CodeControlPlaneDenied {
				t.Fatalf("gate called %q control plane: %+v", tc.path, res)
			}
			if resolverCode == isolation.CodeControlPlaneDenied {
				t.Fatalf("resolver called %q control plane", tc.path)
			}
		})
	}
}
