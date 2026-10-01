package contract

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// frozenNonSecretTitles lists stable non-secret ladder labels.
var frozenNonSecretTitles = map[string]bool{
	hitl.TitleAllowOnce:           true,
	hitl.TitleAllowFor1Day:        true,
	hitl.TitleAllowForThisChat:    true,
	hitl.TitleAllowForThisProject: true,
	hitl.TitleAllowOnThisDevice:   true,
	hitl.TitleQuietForThisChat:    true,

	hitl.TitleAllowCommandNetworkForThisChat: true,
}

func TestRecommendedOptionOnlySetInPlanConstruction(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var faceWriters []string

	corpus, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon", "internal"))
	contractcheck.FailErr(t, "load internal Go corpus", err)
	for _, source := range corpus.Files() {
		base := filepath.Base(source.Path)
		if source.IsTest || base == "approval_plan.go" || base == "checkpoint_wire.go" {
			continue
		}
		rel, _ := filepath.Rel(root, source.Path)
		ast.Inspect(source.AST, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.KeyValueExpr:
				if key, ok := node.Key.(*ast.Ident); ok && key.Name == "RecommendedOptionID" {
					faceWriters = append(faceWriters, rel+": RecommendedOptionID literal")
				}
			case *ast.AssignStmt:
				for _, lhs := range node.Lhs {
					if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel != nil && sel.Sel.Name == "RecommendedOptionID" {
						faceWriters = append(faceWriters, rel+": RecommendedOptionID assign")
					}
				}
			}
			return true
		})
	}

	if len(faceWriters) > 0 {
		t.Fatalf("face may only be computed in approval_plan.go:\n  %s", strings.Join(faceWriters, "\n  "))
	}
}

// TestPlanOptionTitlesAreFrozen sweeps every ordinary ladder mint site.
func TestPlanOptionTitlesAreFrozen(t *testing.T) {
	t.Parallel()
	store := mustApprovalStore(t)
	approvals := settings.NewRuleApprovalGate(store, settings.NoSources())
	action := hitl.ProposedAction{
		Tool: "network", Args: map[string]any{"host": "api.example.com"},
		SessionID: "sess-titles", ProjectID: "proj-titles", ProjectDir: "/tmp/proj",
	}
	checkOffers := func(label string, offers []hitl.ApprovalGrantOffer) {
		t.Helper()
		if len(offers) == 0 {
			t.Fatalf("%s: empty ladder", label)
		}
		for _, offer := range offers {
			if !frozenNonSecretTitles[offer.Title] {
				t.Fatalf("%s: title %q not in frozen set", label, offer.Title)
			}
		}
	}

	result := &hitl.ApprovalResult{Decision: &gate.Decision{Primary: api.GateUserRule}}
	checkOffers("host", approvals.GrantOffers(action, result))

	pathAction := hitl.ProposedAction{
		Tool: "write", Files: []string{"/tmp/proj/a.txt"}, SessionID: "sess-titles",
		ProjectID: "proj-titles", ProjectDir: "/tmp/proj",
	}
	checkOffers("path", settings.GrantedPathOffers(pathAction, gate.FileTarget{
		Path: "/tmp/proj/a.txt", Mode: gate.ModeWrite,
	}, &gate.Decision{Primary: api.GateSensitiveLocation}, nil))

	cmd := hitl.ProposedAction{
		Tool: "command", Command: "git status", Args: map[string]any{"command": "git status"},
		SessionID: "sess-titles", ProjectID: "proj-titles", ProjectDir: "/tmp/proj",
	}
	checkOffers("exact action", approvals.GrantOffers(cmd, &hitl.ApprovalResult{
		Decision: &gate.Decision{Primary: api.GateExplicitApprovalRequest},
	}))

	checkOffers("socket", tools.SocketExecutionGrantOffers(cmd, []confine.SocketGrant{{
		ApprovedPath: "/tmp/svc.sock", ResolvedPath: "/private/tmp/svc.sock",
	}}))

	checkOffers("direct ip", tools.DirectIPExecutionGrantOffers(cmd, hitl.DirectIPLease{
		ActionDigest: "action-a", RequestDigest: "req-a", ConfinementDigest: "conf-a",
		DeclaredDestinations: []string{"udp://1.2.3.4:123"}, CommandSummary: "ntpdate",
	}))

	hr := action
	hr.HostResources = []string{"camera"}
	checkOffers("host resource card", approvals.GrantOffers(hr, &hitl.ApprovalResult{
		Decision: &gate.Decision{Primary: api.GateUserRule}, HostResourceApproval: true,
	}))

	quiet := hitl.QuietOptions(cmd, &gate.Decision{
		Primary:   api.GateAuthorityMisuse,
		ReasonKey: "authority_misuse:aws-cli/s3-remove-bucket",
	}, nil, nil)
	if len(quiet) == 0 {
		t.Fatal("quiet ladder empty")
	}
	for _, opt := range quiet {
		if !frozenNonSecretTitles[opt.Title] {
			t.Fatalf("quiet: title %q not in frozen set", opt.Title)
		}
	}
}

// TestTwoSubjectCardsCarryGroups pins absorbed and host-resource second ladders.
func TestTwoSubjectCardsCarryGroups(t *testing.T) {
	t.Parallel()
	approvals := settings.NewRuleApprovalGate(mustApprovalStore(t), settings.NoSources())
	action := hitl.ProposedAction{
		Tool: "network", Args: map[string]any{"host": "api.example.com"},
		SessionID: "sess-groups", ProjectID: "proj-groups", ProjectDir: "/tmp/proj",
		HostResources: []string{"camera"},
	}
	result := &hitl.ApprovalResult{Decision: &gate.Decision{Primary: api.GateUserRule}, HostResourceApproval: true}

	offered := approvals.GrantOffers(action, result)
	var sawHostResources bool
	for _, offer := range offered {
		if offer.Group == hitl.GroupHostResources {
			sawHostResources = true
		}
	}
	if !sawHostResources {
		t.Fatal("host-resource-riding card must tag the second ladder Host resources")
	}

	absorbed := approvals.AbsorbedGrantOffers(action, result)
	if len(absorbed) == 0 {
		t.Fatal("absorbed ladder empty")
	}
	for _, offer := range absorbed {
		if offer.Group != hitl.GroupAlsoAllow && offer.Group != hitl.GroupHostResources {
			t.Fatalf("absorbed offer group = %q, want Also allow or Host resources", offer.Group)
		}
	}
}

func mustApprovalStore(t *testing.T) *settings.ApprovalStore {
	t.Helper()
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	contractcheck.FailErr(t, "NewApprovalStoreAt", err)
	return store
}
