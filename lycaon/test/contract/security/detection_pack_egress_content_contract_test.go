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
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestDetectionPackEgressSuffixesAnchored: endswith values begin with '.'; proofs both ways.
func TestDetectionPackEgressSuffixesAnchored(t *testing.T) {
	t.Parallel()
	dir := config.DetectionPacksDir.Join("egress-providers", "rules")
	ents, err := config.List(dir)
	testutil.FailErr(t, "ReadDir egress-providers", err)
	for _, ent := range ents {
		name := ent.Name()
		if !strings.HasSuffix(name, ".yml") {
			continue
		}
		raw := readRuleYAML(t, dir.Join(name))
		det, _ := raw["detection"].(map[string]any)
		for selName, selVal := range det {
			if selName == "condition" {
				continue
			}
			sel, ok := selVal.(map[string]any)
			if !ok {
				continue
			}
			for field, fv := range sel {
				base := strings.Split(field, "|")[0]
				mod := ""
				if i := strings.Index(field, "|"); i >= 0 {
					mod = field[i+1:]
				}
				if base != "DestinationHostname" || mod != "endswith" {
					continue
				}
				for _, s := range yamlStringList(fv) {
					if !strings.HasPrefix(s, ".") {
						t.Errorf("%s: endswith %q must begin with '.'", name, s)
					}
				}
			}
		}
	}

	cat := loadBundledDetectionCatalog(t)
	m := detectionpack.NewMatcher(cat)
	if hit, ok := m.MatchEgress(contractEgressEvent("evil-1password.com")); ok {
		t.Fatalf("evil-1password.com must not match, got %+v", hit)
	}
	hit, ok := m.MatchEgress(contractEgressEvent("169.254.169.254"))
	if !ok || hit.PackID != "egress-providers" || hit.RuleTitle == "" {
		t.Fatalf("instance metadata host must match, got %+v ok=%v", hit, ok)
	}
	// Medium control-plane rules cover cloud SDK dials that never show a CLI.
	hit, ok = m.MatchEgress(contractEgressEvent("secretsmanager.us-east-1.amazonaws.com"))
	if !ok || hit.PackID != "egress-providers" || hit.RuleID != "1da72f72-30ee-4a9c-a9d6-a1c2a23519e9" {
		t.Fatalf("AWS control-plane host must match aws-control-plane, got %+v ok=%v", hit, ok)
	}
	hit, ok = m.MatchEgress(contractEgressEvent("storage.googleapis.com"))
	if !ok || hit.RuleID != "872fa6f0-6f35-4c23-b622-31ab139086d5" {
		t.Fatalf("GCP control-plane host must match, got %+v ok=%v", hit, ok)
	}
	hit, ok = m.MatchEgress(contractEgressEvent("management.azure.com"))
	if !ok || hit.RuleID != "6f263710-c664-4500-8d4f-840ddf489b66" {
		t.Fatalf("Azure control-plane host must match, got %+v ok=%v", hit, ok)
	}
	hit, ok = m.MatchEgress(contractEgressEvent("api.digitalocean.com"))
	if !ok || hit.RuleID != "b16a2f3f-3c58-465c-aae7-ab8e4899e864" {
		t.Fatalf("IaaS-other host must match, got %+v ok=%v", hit, ok)
	}
	hit, ok = m.MatchEgress(contractEgressEvent("api.cloudflare.com"))
	if !ok || hit.RuleID != "fcb66f15-4101-4fe8-aa39-20f8cb62a180" {
		t.Fatalf("PaaS/edge host must match, got %+v ok=%v", hit, ok)
	}
	hit, ok = m.MatchEgress(contractEgressEvent("app.terraform.io"))
	if !ok || hit.RuleID != "9bbbb2a9-9ddd-444a-a19c-96fd65119f83" {
		t.Fatalf("IaC remote host must match, got %+v ok=%v", hit, ok)
	}
	hit, ok = m.MatchEgress(contractEgressEvent("api.openai.com"))
	if !ok || hit.RuleID != "3ba2166d-9ce3-4542-b8b7-60f73018bc27" {
		t.Fatalf("data/AI cloud host must match, got %+v ok=%v", hit, ok)
	}
	hit, ok = m.MatchEgress(contractEgressEvent("api.github.com"))
	if !ok || hit.RuleID != "b43310e5-f6e8-460d-92dc-c3254c31bc4e" {
		t.Fatalf("forge API host must match, got %+v ok=%v", hit, ok)
	}
	hit, ok = m.MatchEgress(contractEgressEvent("upload.pypi.org"))
	if !ok || hit.RuleID != "841bb2ab-1c6a-4f0f-8b9a-05c3204b8299" {
		t.Fatalf("package publish host must match, got %+v ok=%v", hit, ok)
	}
	hit, ok = m.MatchEgress(contractEgressEvent("api.linear.app"))
	if !ok || hit.RuleID != "2b96abb0-5105-4ee5-9b06-946046480311" {
		t.Fatalf("dev SaaS host must match, got %+v ok=%v", hit, ok)
	}
	for _, quiet := range []string{"www.cloudflare.com", "github.com", "gitlab.com", "registry.npmjs.org", "pypi.org"} {
		if hit, ok := m.MatchEgress(contractEgressEvent(quiet)); ok {
			t.Fatalf("%s must stay quiet, got %+v", quiet, hit)
		}
	}
}

// TestDetectionPackApiActionFromArgvAST: no regexp, casing table, or map literal; aws-only populate.
func TestDetectionPackApiActionFromArgvAST(t *testing.T) {
	t.Parallel()
	path := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "detectionpack", "event.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	testutil.FailErr(t, "ParseFile event.go", err)
	var fn *ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if ok && fd.Name != nil && fd.Name.Name == "ApiActionFromArgv" {
			fn = fd
			break
		}
	}
	if fn == nil || fn.Body == nil {
		t.Fatal("ApiActionFromArgv not found")
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && id.Name == "regexp" {
				t.Fatal("ApiActionFromArgv must not use regexp")
			}
		case *ast.CompositeLit:
			if _, ok := x.Type.(*ast.MapType); ok {
				t.Fatal("ApiActionFromArgv must not contain a map literal")
			}
		case *ast.CallExpr:
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			// Allow strings.ToLower / HasPrefix / join; forbid casing tables via maps already.
			if pkg.Name == "strings" && (sel.Sel.Name == "ToUpper" || sel.Sel.Name == "Title") {
				t.Fatalf("ApiActionFromArgv unexpected casing helper %s", sel.Sel.Name)
			}
		}
		return true
	})

	// Behavior: aws-only populate.
	if got := detectionpack.ApiActionFromArgv("aws", []string{"aws", "iam", "create-access-key"}); got != "iam:create-access-key" {
		t.Fatalf("aws ApiAction = %q", got)
	}
	if got := detectionpack.ApiActionFromArgv("gcloud", []string{"gcloud", "iam", "service-accounts", "create"}); got != "" {
		t.Fatalf("non-aws ApiAction = %q", got)
	}
	ev := contractToolEvent("command", "gcloud compute instances delete x", "/p", true, "proxy", "s")
	if len(ev.ApiAction) != 0 {
		t.Fatalf("gcloud event ApiAction=%v", ev.ApiAction)
	}
	ev2 := contractToolEvent("command", "aws s3 rb s3://b", "/p", true, "proxy", "s")
	if len(ev2.ApiAction) == 0 || ev2.ApiAction[0] != "s3:rb" {
		t.Fatalf("aws event ApiAction=%v", ev2.ApiAction)
	}
}

// TestDetectionPackConnectHoldComposition exercises exported confine setters + DecideAttributedHost.
// Not parallel: mutates process-wide egress broker state.
func TestDetectionPackConnectHoldComposition(t *testing.T) {
	confine.SetEgressPosture(confine.PostureObserve)
	t.Cleanup(func() { confine.SetEgressPosture(confine.PostureObserve) })
	confine.SetEgressRuleEvaluator(nil)
	t.Cleanup(func() { confine.SetEgressRuleEvaluator(nil) })
	confine.SetDetectionApprovalPosture(func(confine.EgressCommand) string { return "balanced" })
	t.Cleanup(func() { confine.SetDetectionApprovalPosture(nil) })

	stub := stubConfineEgressDetection{
		ok: true,
		match: confine.EgressDetectionCitation{
			PackID: "egress-providers", RuleID: "r1", RuleTitle: "iam", Level: "critical",
		},
	}
	confine.SetEgressDetectionSource(stub)
	t.Cleanup(func() { confine.SetEgressDetectionSource(nil) })

	cmd := confine.EgressCommand{SessionID: "connect-hold-s1", ToolCallID: "tc1"}

	// Observe + escalating match parks CONNECT (resolver asked with detection reason).
	asked := 0
	confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, ep egressproxy.Endpoint, det *confine.EgressDetectionCitation) bool {
		asked++
		if det == nil || det.Level != "critical" {
			t.Errorf("expected detection citation, got %+v", det)
		}
		return ep.Host == "iam.amazonaws.com"
	})
	t.Cleanup(func() { confine.SetEgressResolver(nil) })
	if !confine.DecideAttributedHost(context.Background(), cmd, "iam.amazonaws.com") {
		t.Fatal("detection hold should park then allow")
	}
	if asked != 1 {
		t.Fatalf("asked=%d, want one hold", asked)
	}

	// Host deny wins; detection cannot soften.
	asked = 0
	confine.SetEgressRuleEvaluator(func(_ context.Context, _ confine.EgressCommand, host string) confine.EgressRuleResult {
		if host == "evil.example" {
			return confine.EgressRuleResult{Effect: confine.EgressRuleDeny, Pattern: "evil.example"}
		}
		return confine.EgressRuleResult{}
	})
	if confine.DecideAttributedHost(context.Background(), confine.EgressCommand{SessionID: "connect-hold-s2"}, "evil.example") {
		t.Fatal("host deny must win")
	}
	if asked != 0 {
		t.Fatalf("resolver asked under host deny: %d", asked)
	}

	// Host ask policy cannot skip a detection hold.
	asked = 0
	confine.SetEgressRuleEvaluator(func(_ context.Context, _ confine.EgressCommand, host string) confine.EgressRuleResult {
		if host == "ok.example" {
			return confine.EgressRuleResult{Effect: confine.EgressRuleAsk, Pattern: "ok.example"}
		}
		return confine.EgressRuleResult{}
	})
	confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, _ egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
		asked++
		return true
	})
	if !confine.DecideAttributedHost(context.Background(), confine.EgressCommand{SessionID: "connect-hold-s3"}, "ok.example") {
		t.Fatal("approved detection hold should proceed")
	}
	if asked != 1 {
		t.Fatalf("detection ask count=%d, want 1", asked)
	}

	// Authored host policy cannot turn a later detection into an allow.
	asked = 0
	confine.SetEgressRuleEvaluator(func(_ context.Context, _ confine.EgressCommand, host string) confine.EgressRuleResult {
		return confine.EgressRuleResult{Effect: confine.EgressRuleAsk, Pattern: host}
	})
	confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, _ egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
		asked++
		return true
	})
	if !confine.DecideAttributedHost(context.Background(), confine.EgressCommand{SessionID: "connect-hold-s4"}, "registry.example") {
		t.Fatal("first CONNECT should allow after card")
	}
	if asked != 1 {
		t.Fatalf("asked=%d", asked)
	}
	if !confine.DecideAttributedHost(context.Background(), confine.EgressCommand{SessionID: "connect-hold-s5"}, "registry.example") {
		t.Fatal("second approved detection hold should proceed")
	}
	if asked != 2 {
		t.Fatalf("later detection must re-ask: asked=%d", asked)
	}

	// Detection holds reuse CheckpointKindToolApproval — no second kind.
	kindsPath := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "pkg", "api", "checkpoint_types.go")
	data, err := os.ReadFile(kindsPath)
	testutil.FailErr(t, "read checkpoint_types.go", err)
	if strings.Contains(string(data), "CheckpointKindDetection") {
		t.Fatal("must not mint a second checkpoint kind for detection holds")
	}
	_ = api.CheckpointKindToolApproval // pin the existing kind remains defined
}
