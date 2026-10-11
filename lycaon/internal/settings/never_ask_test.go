package settings_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func neverAskGate(t *testing.T, neverAsk bool) (hitl.ApprovalGate, string) {
	t.Helper()
	tmp := t.TempDir()
	// Strict: never_ask must win over the loudest posture, not merely
	// agree with a quiet one.
	body := "approval_posture: " + string(gate.PostureStrict) + "\nrules:\n"
	if neverAsk {
		// `rules:` parses as null, so a key at column 0 after it is a sibling.
		body += "never_ask: true\n"
	}
	stageBundledApprovalsYAML(t, body)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "approval store", err)
	return settings.NewRuleApprovalGate(store, settings.NoSources()), filepath.Join(tmp, "project")
}

// The switch buys silence: everything the gate would have asked about auto-approves,
// including the substrate path-escape that survives every posture.
func TestNeverAskSilencesEveryAsk(t *testing.T) {
	gate, proj := neverAskGate(t, true)
	for _, action := range []hitl.ProposedAction{
		{
			Invocation: hitl.ActionInvocation{Tool: "write", Files: []string{"/etc/hosts"}},
			Scope:      hitl.ActionScope{ProjectDir: proj},
		},
		{
			Invocation: hitl.ActionInvocation{Tool: "command", Args: map[string]any{"command": "git push origin main"}},
			Scope:      hitl.ActionScope{ProjectDir: proj},
		},
		{
			Invocation: hitl.ActionInvocation{Tool: "command", Args: map[string]any{"command": "aws s3 rm --recursive s3://b"}},
			Scope:      hitl.ActionScope{ProjectDir: proj},
		},
		{
			Invocation: hitl.ActionInvocation{Tool: "mcp__server__tool"},
			Scope:      hitl.ActionScope{ProjectDir: proj},
		},
	} {
		res, err := gate.Evaluate(context.Background(), action)
		testutil.FailErr(t, "evaluate", err)
		if !res.AutoApproved() || res.Required() {
			t.Fatalf("never_ask must auto-approve %s: %+v", action.Invocation.Tool, res)
		}
	}
}

// Hard denies remain active when approval prompts are disabled.
func TestNeverAskDoesNotClearHardDenies(t *testing.T) {
	gate, proj := neverAskGate(t, true)
	configDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	for _, rel := range []string{
		filepath.Join(configDir, "approvals.yaml"),
		filepath.Join(configDir, "mcp.yaml"),
		filepath.Join(configDir, "extensions.yaml"),
	} {
		res, err := gate.Evaluate(context.Background(), hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write",
Files: []string{rel},
},
Scope: hitl.ActionScope{
ProjectDir: proj,
},
})
		testutil.FailErr(t, "evaluate "+rel, err)
		if !res.Denied {
			t.Fatalf("never_ask must not clear the hard deny on %s: %+v", rel, res)
		}
	}
}

// The default is off, and the same actions ask without it — otherwise the test
// above would pass against a gate that never asks for unrelated reasons.
func TestWithoutNeverAskStrictStillAsks(t *testing.T) {
	gate, proj := neverAskGate(t, false)
	res, err := gate.Evaluate(context.Background(), hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "true"},
},
Scope: hitl.ActionScope{
ProjectDir: proj,
},
})
	testutil.FailErr(t, "evaluate", err)
	if !res.Required() {
		t.Fatalf("an unconfined process must ask when never_ask is unset: %+v", res)
	}
}

// A project overlay is repo content. It may restore asking; it may never take it
// away, which is the whole reason this resolves on the global layer.
func TestProjectOverlayCannotSetNeverAsk(t *testing.T) {
	tmp := t.TempDir()
	stageBundledApprovalsPosture(t, gate.PostureBalanced)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "approval store", err)

	proj := filepath.Join(tmp, "project")
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(proj, settingsoverlay.DirName()), 0o755))
	testutil.FailErr(t, "write overlay", os.WriteFile(
		filepath.Join(proj, settingsoverlay.DirName(), "approvals.yaml"),
		[]byte("never_ask: true\nrules:\n"), 0o644))

	cfg := store.Get("project", settings.ProjectRef{Dir: proj})
	if cfg.NeverAsk != nil && *cfg.NeverAsk {
		t.Fatal("a project overlay must not be able to disable approvals")
	}
}
