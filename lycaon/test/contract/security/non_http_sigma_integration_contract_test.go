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
	"github.com/lycaon/lycaon/internal/authzcontext"
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
		Tool:       "command",
		Args:       map[string]any{"command": "echo hi"},
		ProjectDir: "/proj",
		SessionID:  "s1",
		Contained: hitl.Contained{
			FSJailed:          true,
			Egress:            hitl.ContainedEgressDirectIP,
			DirectIP:          true,
			SocketPathsDigest: "sock-digest",
			SocketCount:       2,
			Roots:             []string{"/proj"},
		},
		ActionID:             "call-1",
		DirectIPRequested:    true,
		Visibility:           "unobserved",
		DeclaredDestinations: []string{"db.example.com:22"},
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

func TestNonHTTPContractPreSpawnDetectionOverlaySeamForSocketAndDirect(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	// Detection matches enter the decision through gate facts.
	facts := contractcheck.ReadRepoFile(t, root, "lycaon/internal/settings/gate_facts.go")
	assemble := mustFindFunc(t, facts, "gate_facts.go", "factsForAction")
	body := facts[assemble.Body.Pos()-1 : assemble.Body.End()]
	if !strings.Contains(body, "g.sources.Detections") || !strings.Contains(body, ".MatchAction(") ||
		!strings.Contains(body, "facts.Detection") {
		t.Fatal("fact assembly must record the detection match for the gate")
	}
	// The producer bit distinguishes an unwired source from an empty scan.
	if !strings.Contains(body, "facts.Ran |= gate.ProducerDetection") {
		t.Fatal("detection must report as a producer only when a source is wired")
	}
	gateSrc := contractcheck.ReadRepoFile(t, root, "lycaon/internal/settings/rule_gate.go")
	eval := mustFindFunc(t, gateSrc, "rule_gate.go", "Evaluate")
	evalBody := gateSrc[eval.Body.Pos()-1 : eval.Body.End()]
	if !strings.Contains(evalBody, "gate.Evaluate(facts") {
		t.Fatal("approval Evaluate must decide through the single gate evaluation")
	}

	exec := contractcheck.ReadRepoFile(t, root, "lycaon/internal/tools/executor_impl.go")
	if !strings.Contains(exec, "applyPreInvokeBoundary") {
		t.Fatal("executor must run capability preflight before spawn/policy")
	}
	sock := contractcheck.ReadRepoFile(t, root, "lycaon/internal/tools/socket_capability.go")
	boundary := contractcheck.ReadRepoFile(t, root, "lycaon/internal/tools/invocation_boundary.go")
	pre := mustFindFunc(t, boundary, "invocation_boundary.go", "applyPreInvokeBoundary")
	pbody := boundary[pre.Body.Pos()-1 : pre.Body.End()]
	if !strings.Contains(pbody, "preflightSocketCapability") || !strings.Contains(pbody, "preflightDirectIPCapability") {
		t.Fatal("pre-invoke boundary must cover socket and direct adapters")
	}
	if !strings.Contains(pbody, "policy.Evaluate") {
		t.Fatal("socket/direct outer actions must still reach profile policy after capability resolution")
	}
	if !strings.Contains(pbody, "BoundaryApprovalSatisfied") {
		t.Fatal("capability resolution must suppress duplicate approval-gate evaluation")
	}
	if !strings.Contains(sock, "evaluatePreSpawn") {
		t.Fatal("capability cards must consume the single pre-spawn approval result")
	}
	direct := contractcheck.ReadRepoFile(t, root, "lycaon/internal/tools/direct_ip_capability.go")
	if !strings.Contains(direct, "evaluatePreSpawn") {
		t.Fatal("direct cards must consume the single pre-spawn approval result")
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
		Tool:       "command",
		Args:       map[string]any{"command": "echo hi"},
		Contained:  hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{"/tmp/proj"}},
		ProjectDir: "/tmp/proj",
		SessionID:  "s1",
	}
	// Family host leases leave exact-action detection asks intact.
	hostGrant := hitl.ApprovalGrant{
		ID: "grant_host_family_sigma", Scope: hitl.ApprovalGrantScopeChat,
		ChatSessionID: action.ChatSession(), ProjectDir: action.ProjectDir,
		Title: hitl.TitleAllowForThisChat, Coverage: "connections to `api.example.com`",
		ExpiresWhen: hitl.ExpiresWhenChatDeleted,
		Predicate:   hitl.ApprovalGrantPredicate{Category: string(settings.ApprovalCategoryHost), Pattern: "api.example.com"},
		Witness: hitl.ApprovalGrantWitness{
			FSJailed: action.Contained.FSJailed, Egress: action.Contained.Egress,
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

func TestNonHTTPContractCapabilityAndDetectionRemainSeparateRecords(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	actions := authzcontext.AllEventActions()
	var hasCap, hasDet bool
	for _, a := range actions {
		switch a {
		case authzcontext.EventActionCapabilityApplied, authzcontext.EventActionCapabilityGranted:
			hasCap = true
		case authzcontext.EventActionDetectionResolved:
			hasDet = true
		default:
		}
	}
	if !hasCap || !hasDet {
		t.Fatal("authz vocabulary must keep capability_* and detection_resolved distinct")
	}

	// Detection resolution commits with its decision.
	seal := contractcheck.ReadRepoFile(t, root, "lycaon/internal/hitl/resolution_seal.go")
	if !strings.Contains(seal, "sealDetectionResolvedTx") {
		t.Fatal("detection resolution must remain a separate record path sealed with the decision")
	}
	if !strings.Contains(seal, "authzledger.ActionDetectionResolved") {
		t.Fatal("the detection seal must write the detection_resolved action, not a capability action")
	}
	sock := contractcheck.ReadRepoFile(t, root, "lycaon/internal/tools/socket_capability.go")
	if !strings.Contains(sock, "recordCapability") {
		t.Fatal("capability scope must emit capability records separately from detection")
	}
}

func TestNonHTTPContractHTTPSocksStillReachPreDialMatch(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	egress := contractcheck.ReadRepoFile(t, root, "lycaon/internal/confine/confine_egress.go")
	if !strings.Contains(egress, "src.Match(observation)") {
		t.Fatal("egress broker must consult the detection source pre-dial")
	}
	gs := contractcheck.ReadRepoFile(t, root, "lycaon/internal/detectionpack/gate_source.go")
	if !strings.Contains(gs, "func (s *EgressSource) Match(observation EgressObservation") {
		t.Fatal("EgressSource.Match must remain the HTTP/SOCKS adapter")
	}
	if !strings.Contains(gs, "NewEgressEvent") {
		t.Fatal("EgressSource.Match must build egress_observed events")
	}
	_, files := contractcheck.ParseNonTestGoTree(t, filepath.Join(root, "lycaon", "internal", "app"))
	wiring := map[string]bool{
		"wireToolRuntime/wireDetectionPacks":          false,
		"wireDetectionPacks/SetEgressDetectionSource": false,
		"publish/detectionpack.NewEgressSource":       false,
	}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if call, ok := node.(*ast.CallExpr); ok {
					key := fn.Name.Name + "/" + contractcheck.CallName(call)
					if _, required := wiring[key]; required {
						wiring[key] = true
					}
				}
				return true
			})
		}
	}
	for edge, found := range wiring {
		if !found {
			t.Errorf("missing egress detection wiring: %s", edge)
		}
	}
}

func TestNonHTTPContractDirectDeclarationsDoNotClaimEgressObservation(t *testing.T) {
	t.Parallel()
	ea := authzcontext.BuildExternalAccess(authzcontext.ExternalAccessInput{
		Direct:               true,
		DeclaredDestinations: []string{"203.0.113.10:443"},
	})
	if ea.Direct == nil || ea.Direct.ActualDestinationVisibility != api.ExternalAccessVisibilityUnobserved {
		t.Fatalf("direct must be unobserved, got %+v", ea.Direct)
	}
	if len(ea.Endpoints) != 0 {
		t.Fatalf("declared destinations must not become observed endpoints: %+v", ea.Endpoints)
	}
	if len(ea.DeclaredDestinations) != 1 {
		t.Fatalf("declared must stay in declared fields: %+v", ea.DeclaredDestinations)
	}

	root := contractcheck.RepoRoot(t)
	direct := contractcheck.ReadRepoFile(t, root, "lycaon/internal/tools/direct_ip_capability.go")
	if strings.Contains(direct, "egress_observed") || strings.Contains(direct, "NewEgressEvent") || strings.Contains(direct, "EgressObservation") {
		t.Fatal("direct capability must not emit egress_observed or claim destination coverage")
	}
	if !strings.Contains(direct, "DirectIPVisibilityUnobserved") {
		t.Fatal("direct capability must mark destinations unobserved")
	}
}

func TestNonHTTPContractNoNewSigmaEngineInCapabilityPackages(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dirs := []string{
		filepath.Join(root, "lycaon", "internal", "tools"),
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

func TestNonHTTPContractCorrelationIDNeverAuthorizationIdentity(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	// Correlation metadata carries no approval authority.
	banned := []string{
		"lycaon/internal/hitl/grant_key.go",
		"lycaon/internal/session/approvalstate/tool_approval_coalesce.go",
		"lycaon/internal/tools/direct_ip_capability.go",
	}
	banned = append(banned, socketCapabilityPaths...)
	for _, rel := range banned {
		body := contractcheck.ReadRepoFile(t, root, rel)
		if strings.Contains(body, "CorrelationID") || strings.Contains(body, "correlation_id") {
			t.Fatalf("%s must not reference CorrelationID in grant/coalesce identity paths", rel)
		}
	}
	action := hitl.ProposedAction{
		Tool: "command", Args: map[string]any{"command": "echo"}, ProjectDir: "/p",
		Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{"/p"}},
	}
	key := hitl.GrantKey(action)
	if strings.Contains(key, "det_") || strings.Contains(strings.ToLower(key), "correlation") {
		t.Fatalf("GrantKey must not embed correlation material: %q", key)
	}
	// Detection approval identity derives from pack, rule, and level.
	impl := contractcheck.ReadRepoFile(t, root, "lycaon/internal/tools/executor_egress_ask.go")
	fn := "func (s egressAskSubject) withDetection("
	idx := strings.Index(impl, fn)
	if idx < 0 {
		t.Fatal("egressAskSubject.withDetection missing — the detection approval key moved without this contract")
	}
	end := strings.Index(impl[idx:], "\n}")
	if end < 0 {
		t.Fatal("egressAskSubject.withDetection body not found")
	}
	body := impl[idx : idx+end]
	if strings.Contains(body, "CorrelationID") {
		t.Fatal("the detection approval key must not use CorrelationID")
	}
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
