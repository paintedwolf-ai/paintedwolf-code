package contract

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// Sigma remains an additive overlay and receives host-derived capability facts.

func TestNonHTTPContractGateSourceProjectsContainedEgressFromHost(t *testing.T) {
	t.Parallel()
	rule, err := detectionpack.ParseRule([]byte(`title: Contained projection probe
id: 9f3c2a10-6b4e-4d2f-9a1b-0c8e7d6f5a41
description: Confirms that host-derived confinement facts reach Sigma matching.
logsource:
  product: lycaon
  service: tool_exec
level: critical
detection:
  selection:
    Tool: command
    DirectIp: 'true'
    Visibility: unobserved
    DeclaredDestination: db.example.com:22
    Contained: 'false'
    FSJailed: 'true'
  condition: selection
`))
	contractcheck.FailErr(t, "ParseRule", err)
	src := detectionpack.NewGateSource(detectionpack.NewMatcher(&detectionpack.Catalog{Packs: []detectionpack.Pack{{
		ID: "stub", Enabled: true, Rules: []detectionpack.Rule{rule},
	}}}))

	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool:     "command",
			Args:     map[string]any{"command": "echo hi"},
			ActionID: "call-1",
		},
		Scope: hitl.ActionScope{
			ProjectDir: "/proj",
			SessionID:  "s1",
		},
		Execution: hitl.ActionExecution{
			Contained: hitl.Contained{
				FSJailed:          true,
				Egress:            hitl.ContainedEgressDirectIP,
				DirectIP:          true,
				SocketPathsDigest: "sock-digest",
				SocketCount:       2,
				Roots:             []string{"/proj"},
			},
		},
		Egress: hitl.ActionEgress{
			DirectIPRequested:    true,
			Visibility:           "unobserved",
			DeclaredDestinations: []string{"db.example.com:22"},
		},
	}
	_, ok := src.MatchAction(action, "strict")
	if !ok {
		t.Fatal("GateSource must match tool_exec against Contained-bearing action")
	}

	// Contained itself carries socket/direct facts from confine projection.
	synthetic := &confine.Confinement{
		Roots:   []string{t.TempDir()},
		Network: confine.NetworkDirectIP,
		SocketGrants: []confine.SocketGrant{
			{ApprovedPath: "/tmp/a.sock", ResolvedPath: "/tmp/a.sock"},
		},
	}
	c := hitl.ContainedFromConfinement(synthetic, true)
	if !c.DirectIP || c.Egress != hitl.ContainedEgressDirectIP || c.SocketCount != 1 || c.SocketPathsDigest == "" {
		t.Fatalf("Contained must carry DirectIP + socket digest/count from confine: %+v", c)
	}
}

func TestNonHTTPContractCapabilityGrantsCannotSuppressDetection(t *testing.T) {
	src := stubDetectionSource{
		ok: true,
		match: hitl.DetectionMatch{
			PackID: "stub", RuleID: "r", RuleTitle: "everything", Level: "critical",
		},
	}
	approvalGate := sigmaGateWithDetection(t, gate.PostureBalanced, src)
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{"command": "echo hi"},
		},
		Execution: hitl.ActionExecution{
			Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{"/tmp/proj"}},
		},
		Scope: hitl.ActionScope{
			ProjectDir: "/tmp/proj",
			SessionID:  "s1",
		},
	}
	// Family host leases leave exact-action detection asks intact.
	hostGrant := hitl.ApprovalGrant{
		ID: "grant_host_family_sigma", Scope: hitl.ApprovalGrantScopeChat,
		ChatSessionID: action.Scope.ChatSession(), ProjectDir: action.Scope.ProjectDir,
		Title: hitl.TitleAllowForThisChat, Coverage: "connections to `api.example.com`",
		ExpiresWhen: hitl.ExpiresWhenChatDeleted,
		Predicate:   hitl.ApprovalGrantPredicate{Category: string(settings.ApprovalCategoryHost), Pattern: "api.example.com"},
		Witness: hitl.ApprovalGrantWitness{
			FSJailed: action.Execution.Contained.FSJailed, Egress: action.Execution.Contained.Egress,
		},
	}
	_, err := approvalGate.ApplyGrant(hostGrant)
	testutil.FailErr(t, "ApplyGrant", err)
	res, err := approvalGate.Evaluate(context.Background(), action)
	testutil.FailErr(t, "Evaluate", err)
	if res.DetectionCitation == nil || !res.Required() {
		t.Fatalf("detection must reask despite family host lease, got %+v", res)
	}
	if res.AutoApproved() {
		t.Fatalf("detection reask cannot also auto-approve, got %+v", res)
	}
}

func TestNonHTTPContractNoNewSigmaEngineInCapabilityPackages(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dirs := []string{
		filepath.Join(root, "lycaon", "internal", "tools"),
		filepath.Join(root, "lycaon", "internal", "toolexecution"),
		filepath.Join(root, "lycaon", "internal", "capabilityrequest"),
		filepath.Join(root, "lycaon", "internal", "capabilitygrants"),
		filepath.Join(root, "lycaon", "internal", "session"),
	}
	bannedIdents := []string{
		"MatchAction", "MatchEgress", "NewMatcher", "NewEvent", "NewEgressEvent",
		"Escalates", "winningRule", "denySet", "pendingDetection", "correlationID",
	}
	// Capability adapters consume detection outcomes.
	err := filepath.WalkDir(filepath.Join(root, "lycaon", "internal"), func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		inScope := false
		for _, dir := range dirs {
			if strings.HasPrefix(path, dir+string(filepath.Separator)) {
				inScope = true
				break
			}
		}
		if !inScope {
			return nil
		}
		base := filepath.Base(path)
		// Capability-focused files only — not the whole tools package.
		if !strings.Contains(base, "socket_capability") &&
			!strings.Contains(base, "direct_ip_capability") &&
			!strings.Contains(base, "capability_request") &&
			!strings.Contains(path, string(filepath.Separator)+"session"+string(filepath.Separator)+"socket_capability") &&
			!strings.Contains(path, string(filepath.Separator)+"session"+string(filepath.Separator)+"direct_ip_capability") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			for _, b := range bannedIdents {
				if id.Name == b {
					t.Errorf("%s must not implement Sigma engine symbol %s", path, b)
				}
			}
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "walk capability packages", err)
}

func sigmaGateWithDetection(
	t *testing.T,
	posture gate.Posture,
	src settings.DetectionSource,
) hitl.ApprovalGate {
	t.Helper()
	tmp := t.TempDir()
	cfg := struct {
		Posture gate.Posture            `yaml:"approval_posture"`
		Rules   []settings.ApprovalRule `yaml:"rules"`
	}{Posture: posture}
	data, err := yaml.Marshal(cfg)
	testutil.FailErr(t, "marshal approvals", err)
	// Fixture rules replace bundled defaults while device overlays remain file-backed.
	configtest.Overlay(t, map[config.Rel]string{config.SecurityApprovals: string(data)})
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	_ = store.PutGlobal(settings.ApprovalConfig{Posture: posture})
	return settings.NewRuleApprovalGate(store, settings.Sources{Detections: func() settings.DetectionSource { return src }})
}

// A detection's correlation id groups related alerts; it never takes part in
// approval identity. Two reviews differing only in correlation id key and
// cover the same action, while a different rule does not.
func TestNonHTTPContractCorrelationIDNeverAuthorizationIdentity(t *testing.T) {
	t.Parallel()
	match := hitl.DetectionMatch{PackID: "pack", RuleID: "rule-a", RuleTitle: "Rule A", Level: "critical", CorrelationID: "corr-1"}
	decision := &gate.Decision{
		Primary: api.GateAuthorityMisuse,
		Cited:   []gate.Fact{{Gate: api.GateAuthorityMisuse, Key: "detection.rule", Value: "rule-a"}},
	}
	action := func(review hitl.DetectionMatch) hitl.ProposedAction {
		a := hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{Tool: "command", Args: map[string]any{"command": "echo"}, ActionID: "call-1"},
			Scope:      hitl.ActionScope{ProjectDir: "/p", SessionID: "s1"},
			Execution: hitl.ActionExecution{
				Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{"/p"}},
			},
		}
		a.Invocation.CurrentActionReview = &hitl.CurrentActionReview{
			SessionID: "s1", ActionID: "call-1", ActionKey: hitl.ActionKeyForCurrentReview(a),
			Cited: decision.Cited, Detection: &review,
		}
		return a
	}
	other := match
	other.CorrelationID = "corr-2"
	a, b := action(match), action(other)
	if hitl.GrantKey(a) != hitl.GrantKey(b) {
		t.Fatal("correlation id changed GrantKey")
	}
	if !hitl.CurrentActionReviewCovers(a, decision, &other, nil) {
		t.Fatal("a review must cover a citation that differs only in correlation id")
	}
	otherRule := match
	otherRule.RuleID = "rule-b"
	if hitl.CurrentActionReviewCovers(a, decision, &otherRule, nil) {
		t.Fatal("a review of one rule must not cover another rule")
	}
}
