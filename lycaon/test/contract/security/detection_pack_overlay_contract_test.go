package contract

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// stubDetectionSource verifies that matches cannot alter floor decisions.
type stubDetectionSource struct {
	match hitl.DetectionMatch
	ok    bool
}

func (s stubDetectionSource) MatchAction(hitl.ProposedAction, gate.Posture) (hitl.DetectionMatch, bool) {
	return s.match, s.ok
}

// gateWithDetection stages the approval rules under test as the bundled default.
// That is process-global state, so every test that calls it runs sequentially.
func gateWithDetection(
	t *testing.T,
	posture gate.Posture,
	src settings.DetectionSource,
	rules ...settings.ApprovalRule,
) hitl.ApprovalGate {
	t.Helper()
	tmp := t.TempDir()
	cfg := struct {
		Posture gate.Posture            `yaml:"approval_posture"`
		Rules   []settings.ApprovalRule `yaml:"rules"`
	}{Posture: posture, Rules: rules}
	data, err := yaml.Marshal(cfg)
	testutil.FailErr(t, "marshal approvals", err)
	// Fixture rules replace bundled defaults while device overlays remain file-backed.
	configtest.Overlay(t, map[config.Rel]string{config.SecurityApprovals: string(data)})
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	if posture != "" {
		_ = store.PutGlobal(settings.ApprovalConfig{Posture: posture, Rules: rules})
	}
	g := settings.NewRuleApprovalGate(store, settings.Sources{Detections: func() settings.DetectionSource { return src }})
	return g
}

func containedCommand(cmd string) hitl.ProposedAction {
	return hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{"command": cmd},
		},
		Execution: hitl.ActionExecution{
			Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy},
		},
		Scope: hitl.ActionScope{
			ProjectDir: "/tmp/proj",
			SessionID:  "s1",
		},
	}
}

func criticalAlwaysMatch() stubDetectionSource {
	return stubDetectionSource{
		ok: true,
		match: hitl.DetectionMatch{
			PackID: "stub", RuleID: "r", RuleTitle: "everything", Level: "critical",
		},
	}
}

// Even universal critical matches can only add approval requirements.
func TestDetectionOverlayNeverAllowsDenyOrSuppress(t *testing.T) {
	src := criticalAlwaysMatch()

	// Escalating matches add an ask at each tested posture.
	for _, posture := range []gate.Posture{
		gate.PostureLight, gate.PostureBalanced, gate.PostureStrict,
	} {
		approvalGate := gateWithDetection(t, posture, src)
		res, err := approvalGate.Evaluate(context.Background(), containedCommand("echo hi"))
		testutil.FailErr(t, "Evaluate", err)
		if res.Denied {
			t.Fatalf("overlay invented Denied at %s: %+v", posture, res)
		}
		// Critical matches ask or remain required.
		if res.AutoApproved() && !res.Required() {
			// AutoApproved alone is fine only when no detection raised an ask.
			if citesDetection(res) {
				t.Fatalf("overlay set AutoApproved with a Detection citation at %s: %+v", posture, res)
			}
		}
		if res.Required() && res.AutoApproved() {
			t.Fatalf("overlay produced Required+AutoApproved at %s: %+v", posture, res)
		}
	}

	// The raise path applies user-installed quiets after gate evaluation.
	quietGate := gateWithDetection(t, gate.PostureBalanced, src)
	_, _ = quietGate.PutAskQuiet(hitl.AskQuiet{
		ChatSessionID: "sess",
		Key:           "authority_misuse:stub/r",
		Label:         "stub / r",
	}, 0)
	quietRes, err := quietGate.Evaluate(context.Background(), containedCommand("echo hi"))
	testutil.FailErr(t, "Evaluate with quiet installed", err)
	if !quietRes.Required() || !citesDetection(quietRes) {
		t.Fatalf("quiet must not enter Evaluate/facts; got %+v", quietRes)
	}

	// Exact-action leases cover identical argv; family host leases leave the ask intact.
	gateGranted := gateWithDetection(t, gate.PostureBalanced, src)
	action := containedCommand("echo hi")
	offer := hitl.ExactActionSetOffer(action, []string{hitl.GrantKey(action)})
	_, err = gateGranted.ApplyGrant(offer.Grant)
	testutil.FailErr(t, "ApplyGrant", err)
	res, err := gateGranted.Evaluate(context.Background(), action)
	testutil.FailErr(t, "Evaluate granted", err)
	if res.Required() {
		t.Fatalf("exact-action lease must silence detection for the identical argv, got %+v", res)
	}
	if res.Denied {
		t.Fatalf("session-granted action must not deny, got %+v", res)
	}
	hostAction := action
	hostAction.Invocation.Args = map[string]any{"command": "echo hi", "host": "api.example.com"}
	hostGrant := hitl.ApprovalGrant{
		Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: hostAction.Scope.ChatSession(),
		ProjectDir: hostAction.Scope.ProjectDir, Title: hitl.TitleAllowForThisChat,
		Coverage: "connections to `api.example.com`", ExpiresWhen: hitl.ExpiresWhenChatDeleted,
		Predicate: hitl.ApprovalGrantPredicate{Category: string(settings.ApprovalCategoryHost), Pattern: "api.example.com"},
		Witness: hitl.ApprovalGrantWitness{
			FSJailed: hostAction.Execution.Contained.FSJailed, Egress: hostAction.Execution.Contained.Egress,
		},
	}
	hostGrant.ID = "grant_host_family_fixture"
	gateFamily := gateWithDetection(t, gate.PostureBalanced, src)
	_, err = gateFamily.ApplyGrant(hostGrant)
	testutil.FailErr(t, "ApplyGrant host", err)
	familyRes, err := gateFamily.Evaluate(context.Background(), action)
	testutil.FailErr(t, "Evaluate with host lease", err)
	if !citesDetection(familyRes) || !familyRes.Required() {
		t.Fatalf("family host lease must not silence detection, got %+v", familyRes)
	}

	gateDeny := gateWithDetection(t, gate.PostureBalanced, src,
		settings.ApprovalRule{
			Category: settings.ApprovalCategoryCommand,
			Pattern:  "rm *",
			Effect:   settings.ApprovalEffectDeny,
		},
	)
	res, err = gateDeny.Evaluate(context.Background(), containedCommand("rm -rf /tmp/x"))
	testutil.FailErr(t, "Evaluate deny", err)
	if !res.Denied {
		t.Fatalf("deny rule must remain Denied, got %+v", res)
	}
	if citesDetection(res) {
		t.Fatalf("Denied result must carry Detection=nil, got %+v", res)
	}
	if res.AutoApproved() {
		t.Fatalf("Denied must not be AutoApproved, got %+v", res)
	}
}

func TestDetectionOverlayNeverSuppressesRequired(t *testing.T) {
	src := criticalAlwaysMatch()
	// Detection matches preserve the existing tool-definition approval.
	approvalGate := gateWithDetection(t, gate.PostureStrict, src)
	res, err := approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "mcp__demo__tool",
			Args: map[string]any{},
		},
		Scope: hitl.ActionScope{
			ProjectDir: t.TempDir(),
			SessionID:  "s1",
		},
	})
	testutil.FailErr(t, "Evaluate", err)
	if !res.Required() {
		t.Fatalf("Strict MCP ask must stay Required, got %+v", res)
	}
	if res.Denied || res.AutoApproved() {
		t.Fatalf("Required ask must not become Denied/AutoApproved: %+v", res)
	}
}

// Empty catalogs report completed scans; absent producers remain unreported.
func TestDetectionOverlayZeroPackParity(t *testing.T) {
	emptyCfg := t.TempDir()
	// An empty bundled directory isolates the zero-pack case.
	configtest.Only(t, map[config.Rel]string{
		config.DetectionPacksDir.Join(".keep"): "",
		config.SecurityApprovals:               "rules: []\n",
	})
	cat, err := detectionpack.LoadCatalog(detectionInput(t, emptyCfg, ""))
	testutil.FailErr(t, "LoadCatalog empty", err)
	m := detectionpack.NewMatcher(cat)

	actions := []hitl.ProposedAction{
		containedCommand("echo hi"),
		containedCommand("aws s3 ls"),
		containedCommand("terraform plan"),
		{Invocation: hitl.ActionInvocation{Tool: "read", Files: []string{"a.go"}}, Scope: hitl.ActionScope{ProjectDir: t.TempDir(), SessionID: "s1"}},
	}

	for _, posture := range []gate.Posture{
		gate.PostureLight, gate.PostureBalanced, gate.PostureStrict,
	} {
		noPackGate := gateWithDetection(t, posture, detectionpack.NewGateSource(nil))
		emptyGate := gateWithDetection(t, posture, detectionpack.NewGateSource(m))
		for _, action := range actions {
			a, err := noPackGate.Evaluate(context.Background(), action)
			testutil.FailErr(t, "no-pack Evaluate", err)
			b, err := emptyGate.Evaluate(context.Background(), action)
			testutil.FailErr(t, "empty Evaluate", err)
			if !approvalResultsEqual(a, b) {
				t.Fatalf("zero-pack parity failed posture=%s action=%+v\nno-pack=%+v\nempty=%+v",
					posture, action.Invocation.Tool, a, b)
			}
		}
	}
}

// An absent detector leaves facts incomplete at every posture.
func TestUnwiredDetectionEngineAsksRatherThanPassing(t *testing.T) {
	configtest.Only(t, map[config.Rel]string{
		config.DetectionPacksDir.Join(".keep"): "",
		config.SecurityApprovals:               "rules: []\n",
	})
	for _, posture := range []gate.Posture{
		gate.PostureLight, gate.PostureBalanced, gate.PostureStrict,
	} {
		unwired := gateWithDetection(t, posture, nil)
		res, err := unwired.Evaluate(context.Background(), containedCommand("echo hi"))
		testutil.FailErr(t, "unwired Evaluate", err)
		if res == nil || !res.Required() {
			t.Fatalf("posture=%s: an unwired detection engine passed the action: %+v", posture, res)
		}
		if got := res.Decision.Primary; got != api.GateIncompleteFacts {
			t.Fatalf("posture=%s: gate = %q, want %q", posture, got, api.GateIncompleteFacts)
		}
	}
}

func approvalResultsEqual(a, b *hitl.ApprovalResult) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Required() == b.Required() &&
		a.Denied == b.Denied &&
		a.AutoApproved() == b.AutoApproved() &&
		a.DenyCode == b.DenyCode &&
		a.Gate() == b.Gate() &&
		(citesDetection(a) == citesDetection(b))
}

// Detection results enter decisions through fact assembly.
func TestDetectionOverlaySingleSeamAST(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "internal", "settings")
	entries, err := os.ReadDir(dir)
	contractcheck.FailErr(t, "read settings dir", err)

	var readSites []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src := contractcheck.ReadRepoFile(t, root, filepath.Join("lycaon", "internal", "settings", e.Name()))
		for i, line := range strings.Split(src, "\n") {
			if strings.Contains(line, "sources.Detections") {
				readSites = append(readSites, e.Name()+":"+strconv.Itoa(i+1))
			}
		}
	}
	if len(readSites) != 1 {
		t.Fatalf("detection source must be read exactly once, got %v", readSites)
	}
	if !strings.HasPrefix(readSites[0], "gate_facts.go:") {
		t.Fatalf("the one detection read must be fact assembly, got %s", readSites[0])
	}
}
func TestDetectionOverlayBundledPackStillAskOnly(t *testing.T) {
	cat, err := detectionpack.LoadCatalog(detectionInput(t, t.TempDir(), ""))
	testutil.FailErr(t, "LoadCatalog", err)
	src := detectionpack.NewGateSource(detectionpack.NewMatcher(cat))
	approvalGate := gateWithDetection(t, gate.PostureBalanced, src)

	res, err := approvalGate.Evaluate(context.Background(), containedCommand(
		"aws organizations leave-organization",
	))
	testutil.FailErr(t, "Evaluate", err)
	if res.Denied {
		t.Fatalf("bundled pack must not deny: %+v", res)
	}
	if res.Required() && res.AutoApproved() {
		t.Fatalf("Required+AutoApproved: %+v", res)
	}

	// A matching detection does not override a durable deny.
	gateDeny := gateWithDetection(t, gate.PostureBalanced, src,
		settings.ApprovalRule{
			Category: settings.ApprovalCategoryCommand,
			Pattern:  "aws *",
			Effect:   settings.ApprovalEffectDeny,
		},
	)
	res, err = gateDeny.Evaluate(context.Background(), containedCommand(
		"aws organizations leave-organization",
	))
	testutil.FailErr(t, "Evaluate deny", err)
	if !res.Denied || citesDetection(res) {
		t.Fatalf("Denied must stay clean: %+v", res)
	}
}

func citesDetection(res *hitl.ApprovalResult) bool {
	if res == nil || res.Decision == nil {
		return false
	}
	for _, g := range res.Decision.Gates() {
		if g == api.GateAuthorityMisuse {
			return true
		}
	}
	return false
}
