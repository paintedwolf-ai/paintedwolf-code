package detectionpack

import (
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

var (
	fixtureSemanticsOnce sync.Once
	fixtureSemantics     *ActionSemanticsCatalog
	fixtureSemanticsErr  error
)

func productionFixtureSemantics() (*ActionSemanticsCatalog, error) {
	fixtureSemanticsOnce.Do(func() {
		fixtureSemantics, fixtureSemanticsErr = LoadActionSemantics("")
	})
	return fixtureSemantics, fixtureSemanticsErr
}

// TestShippedPacksRehearseClean validates every shipped fixture.
func TestShippedPacksRehearseClean(t *testing.T) {
	t.Parallel()
	cat, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	semantics, err := productionFixtureSemantics()
	testutil.FailErr(t, "load action semantics", err)
	for _, p := range cat.Packs {
		t.Run(p.ID, func(t *testing.T) {
			t.Parallel()
			for _, finding := range Rehearse(p, loadFixtures(t, p.ID), semantics) {
				t.Error(finding.Message())
			}
		})
	}
}

func anyPackRuleMatches(p Pack, fixture FixtureCase) bool {
	semantics, err := productionFixtureSemantics()
	if err != nil {
		return false
	}
	return anyRuleMatchesCase(p, fixture, semantics)
}

func loadFixtures(t *testing.T, packID string) FixtureCorpus {
	t.Helper()
	data, err := config.Read(config.DetectionPacksDir.Join(packID, PackFixturesFile))
	testutil.FailErr(t, "read fixtures "+packID, err)
	corpus, err := ParseFixtures(data)
	testutil.FailErr(t, "parse fixtures "+packID, err)
	return corpus
}

func TestMatrixCoversUnenumeratedServices(t *testing.T) {
	t.Parallel()
	cat, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	m := NewMatcher(cat)
	cases := []struct {
		cmd, pack, slug string
		level           Level
	}{
		{"aws organizations leave-organization", "aws-cli", "organizations-leave", LevelCritical},
		{"aws s3 rb s3://b", "aws-cli", "s3-remove-bucket", LevelHigh},
		{"az group delete -n my-rg", "azure-cli", "group-delete", LevelCritical},
		{"gcloud projects delete my-old-project", "gcloud-cli", "project-delete", LevelCritical},
		{"terraform destroy -auto-approve", "terraform-cli", "destroy", LevelCritical},
		{"git push --force origin main", "publish-release", "git-force-push", LevelHigh},
	}
	for _, tc := range cases {
		hit, ok := m.Match(testEvent("command", tc.cmd, "/p", true, "proxy", "s"))
		if !ok {
			t.Fatalf("%q: no match", tc.cmd)
		}
		if hit.PackID != tc.pack || hit.Level != tc.level {
			t.Fatalf("%q: hit=%+v want pack=%s level=%s", tc.cmd, hit, tc.pack, tc.level)
		}
		// Confirm the specific slug via pack-local match.
		p, _ := cat.PackByID(tc.pack)
		var found bool
		for _, r := range p.Rules {
			if r.Slug == tc.slug && r.Matches(testEvent("command", tc.cmd, "/p", true, "proxy", "s")) {
				found = true
			}
		}
		if !found {
			t.Fatalf("%q: expected slug %s to match", tc.cmd, tc.slug)
		}
	}
}

func TestMatrixReadOnlySilent(t *testing.T) {
	t.Parallel()
	cat, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	m := NewMatcher(cat)
	cmds := []string{
		"aws ec2 describe-instances",
		"aws s3 ls",
		"aws sts get-caller-identity",
		"aws iam list-users",
		"aws rds describe-db-instances",
		"aws dynamodb list-tables",
		"aws logs describe-log-groups",
		"aws lambda list-functions",
		"aws eks describe-cluster --name x",
		"aws ecs list-clusters",
		"az vm list",
		"az group list",
		"az account show",
		"az aks list",
		"az storage account list",
		"az webapp list",
		"az network vnet list",
		"az sql server list",
		"az cosmosdb list",
		"az monitor metrics list --resource x",
		"gcloud compute instances list",
		"gcloud container clusters list",
		"gcloud sql instances list",
		"gcloud storage buckets list",
		"gcloud functions list",
		"gcloud run services list",
		"gcloud spanner instances list",
		"gcloud pubsub topics list",
		"gcloud logging sinks list",
		"gcloud projects describe my-proj",
		"kubectl get pods",
		"kubectl get nodes",
		"kubectl describe deployment x",
		"kubectl logs pod/x",
		"kubectl top nodes",
		"kubectl explain pod",
		"kubectl get svc",
		"kubectl get ns",
		"oc get pods",
		"kubecolor get pods",
		"aws s3api list-buckets",
		"az policy assignment list",
	}
	if len(cmds) < 40 {
		t.Fatalf("need ≥40 read-only cmds, got %d", len(cmds))
	}
	for _, cmd := range cmds {
		if hit, ok := m.Match(testEvent("command", cmd, "/p", true, "proxy", "s")); ok {
			t.Errorf("read-only %q matched %+v", cmd, hit)
		}
	}
}

func TestMultiStagePositives(t *testing.T) {
	t.Parallel()
	cat, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	m := NewMatcher(cat)

	single := "terraform destroy -auto-approve"
	multi := "cd infra && terraform destroy -auto-approve"
	h1, ok1 := m.Match(testEvent("command", single, "/p", true, "proxy", "s"))
	h2, ok2 := m.Match(testEvent("command", multi, "/p", true, "proxy", "s"))
	if !ok1 || !ok2 || h1.Level != h2.Level || h1.RuleID != h2.RuleID {
		t.Fatalf("terraform multi-stage: single=%+v/%v multi=%+v/%v", h1, ok1, h2, ok2)
	}

	single2 := "aws s3 rb s3://b"
	multi2 := "cd infra && aws s3 rb s3://b"
	a1, ok1 := m.Match(testEvent("command", single2, "/p", true, "proxy", "s"))
	a2, ok2 := m.Match(testEvent("command", multi2, "/p", true, "proxy", "s"))
	if !ok1 || !ok2 || a1.Level != a2.Level || a1.RuleID != a2.RuleID {
		t.Fatalf("aws multi-stage: single=%+v/%v multi=%+v/%v", a1, ok1, a2, ok2)
	}
}

func TestEgressRulesNeverEscalate(t *testing.T) {
	t.Parallel()
	cat, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	p, ok := cat.PackByID("egress-providers")
	if !ok {
		t.Fatal("missing egress-providers")
	}
	for _, r := range p.Rules {
		if r.Source != SourceEgressObserved {
			t.Fatalf("rule %s source=%s", r.Slug, r.Source)
		}
	}
	m := NewMatcher(cat)
	if hit, ok := m.Match(testEvent("command", "curl https://169.254.169.254", "/p", true, "proxy", "s")); ok {
		if hit.PackID == "egress-providers" {
			t.Fatalf("Match returned egress rule: %+v", hit)
		}
	}
}

// An egress rule's only power is holding a CONNECT, so one that cannot escalate
// at any posture is dead weight in the catalog. Which posture it escalates at is
// a severity judgment (a hostname cannot tell a read from a charge, so most of
// this pack sits at medium); being unable to escalate at all is a bug.
func TestEgressEscalatingLevelsHold(t *testing.T) {
	t.Parallel()
	cat, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	p, _ := cat.PackByID("egress-providers")
	for _, r := range p.Rules {
		if !Escalates(r.Level, "strict") {
			t.Fatalf("rule %s level=%s can never hold a CONNECT", r.Slug, r.Level)
		}
	}
}

func TestEgressPackMatches(t *testing.T) {
	t.Parallel()
	cat, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	m := NewMatcher(cat)
	must := func(host, slug string) {
		t.Helper()
		hit, ok := m.MatchEgress(fixtureEgressEvent(host))
		if !ok || hit.PackID != "egress-providers" {
			t.Fatalf("%s: hit=%+v ok=%v", host, hit, ok)
		}
		p, _ := cat.PackByID("egress-providers")
		var found bool
		for _, r := range p.Rules {
			if r.Slug == slug && r.MatchesEgress(fixtureEgressEvent(host)) {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: expected slug %s", host, slug)
		}
	}
	must("169.254.169.254", "instance-metadata")
	must("metadata.google.internal", "instance-metadata")
	must("s3.us-east-1.amazonaws.com", "aws-control-plane")
	must("secretsmanager.us-east-1.amazonaws.com", "aws-control-plane")
	must("storage.googleapis.com", "gcp-control-plane")
	must("us-docker.pkg.dev", "gcp-control-plane")
	must("management.azure.com", "azure-control-plane")
	must("contoso.vault.azure.net", "azure-control-plane")
	must("api.digitalocean.com", "iaas-other-control-plane")
	must("api.cloudflare.com", "paas-edge-control-plane")
	must("app.terraform.io", "iac-remote-control-plane")
	must("api.openai.com", "data-cloud-control-plane")
	must("xy12345.us-east-1.snowflakecomputing.com", "data-cloud-control-plane")
	must("api.clickhouse.cloud", "data-cloud-control-plane")
	must("cloud.getdbt.com", "data-cloud-control-plane")
	must("api.confluent.cloud", "stream-queue-control-plane")
	must("api.backblazeb2.com", "object-storage-control-plane")
	must("abc123.r2.cloudflarestorage.com", "object-storage-control-plane")
	must("api.hubapi.com", "crm-customer-data-control-plane")
	must("api.segment.io", "crm-customer-data-control-plane")
	must("api.github.com", "forge-api-control-plane")
	must("ghcr.io", "container-registry-control-plane")
	must("upload.pypi.org", "package-publish-control-plane")
	must("api.circleci.com", "ci-cd-saas-control-plane")
	must("api.doppler.com", "secrets-auth-saas-control-plane")
	must("api.linear.app", "dev-saas-control-plane")
	// Token issuance endpoints are part of the control plane.
	must("login.microsoftonline.com", "azure-control-plane")
	must("panda-new-kit.ngrok-free.app", "reverse-tunnel")
	must("content.dropboxapi.com", "consumer-file-storage")
	for _, host := range []string{
		"pypi.org", "github.com", "registry.npmjs.org", "evil-1password.com",
		"portal.azure.com", "www.cloudflare.com", "gitlab.com",
		"bitbucket.org", "proxy.golang.org", "index.crates.io",
		// Content fetches, not control calls: forge-api must not fire on these.
		"codeload.github.com", "raw.githubusercontent.com", "objects.githubusercontent.com",
	} {
		if hit, ok := m.MatchEgress(fixtureEgressEvent(host)); ok {
			t.Fatalf("%s should not match, got %+v", host, hit)
		}
	}
}

func TestEquivalentBinariesComplete(t *testing.T) {
	t.Parallel()
	cat, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	for _, p := range cat.Packs {
		if len(p.Equivalents) == 0 {
			continue
		}
		for _, r := range p.Rules {
			images := ruleImageEquals(t, p.ID, r.Slug)
			if len(images) == 0 {
				continue
			}
			for canon, alts := range p.Equivalents {
				members := append([]string{canon}, alts...)
				var hit bool
				for _, img := range images {
					for _, m := range members {
						if img == m {
							hit = true
						}
					}
				}
				if !hit {
					continue
				}
				have := map[string]bool{}
				for _, img := range images {
					have[img] = true
				}
				var missing []string
				for _, m := range members {
					if !have[m] {
						missing = append(missing, m)
					}
				}
				if len(missing) > 0 {
					t.Errorf("%s/%s Image list missing equivalents %v (have %v)", p.ID, r.Slug, missing, images)
				}
			}
		}
	}
}

func ruleImageEquals(t *testing.T, packID, slug string) []string {
	t.Helper()
	data, err := config.Read(config.DetectionPacksDir.Join(packID, "rules", slug+".yml"))
	testutil.FailErr(t, "read rule", err)
	var raw map[string]any
	testutil.FailErr(t, "parse rule", yaml.Unmarshal(data, &raw))
	det, _ := raw["detection"].(map[string]any)
	var out []string
	for k, v := range det {
		if k == "condition" {
			continue
		}
		sel, ok := v.(map[string]any)
		if !ok {
			continue
		}
		for fk, fv := range sel {
			if fk != "Image" {
				continue
			}
			switch x := fv.(type) {
			case string:
				out = append(out, strings.ToLower(x))
			case []any:
				for _, item := range x {
					if s, ok := item.(string); ok {
						out = append(out, strings.ToLower(s))
					}
				}
			}
		}
	}
	return out
}

func TestEquivalentsAreAuthoringMetadataOnly(t *testing.T) {
	t.Parallel()
	cat, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	m1 := NewMatcher(cat)
	// Clear equivalents — matcher must not care.
	for i := range cat.Packs {
		cat.Packs[i].Equivalents = nil
	}
	m2 := NewMatcher(cat)
	cmds := []string{
		"terraform destroy", "tofu destroy", "npm publish", "pnpm publish",
		"aws s3 rb s3://b",
	}
	for _, cmd := range cmds {
		h1, ok1 := m1.Match(testEvent("command", cmd, "/p", true, "proxy", "s"))
		h2, ok2 := m2.Match(testEvent("command", cmd, "/p", true, "proxy", "s"))
		if ok1 != ok2 || h1 != h2 {
			t.Fatalf("%q: with eq %+v/%v without %+v/%v", cmd, h1, ok1, h2, ok2)
		}
	}
}

func TestOpenTofuMatchesTerraform(t *testing.T) {
	t.Parallel()
	cat, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	m := NewMatcher(cat)
	h1, ok1 := m.Match(testEvent("command", "terraform destroy", "/p", true, "proxy", "s"))
	h2, ok2 := m.Match(testEvent("command", "tofu destroy", "/p", true, "proxy", "s"))
	h3, ok3 := m.Match(testEvent("command", "terragrunt destroy", "/p", true, "proxy", "s"))
	if !ok1 || !ok2 || !ok3 || h1.RuleID != h2.RuleID || h1.RuleID != h3.RuleID || h1.Level != LevelCritical {
		t.Fatalf("terraform class: %+v %+v %+v", h1, h2, h3)
	}
	n1, ok1 := m.Match(testEvent("command", "npm publish", "/p", true, "proxy", "s"))
	n2, ok2 := m.Match(testEvent("command", "pnpm publish", "/p", true, "proxy", "s"))
	if !ok1 || !ok2 || n1.RuleID != n2.RuleID {
		t.Fatalf("npm class: %+v %+v", n1, n2)
	}
}

// A declared loopback endpoint is what filters an emulator call. The wrapper
// name never is — see TestResolveEffectReachProgramNameProvesNothing: a name
// that resolved to local reach would be an allow-list an ordinary project write
// could join.
func TestLocalStackFilteredByDeclaredEndpoint(t *testing.T) {
	t.Parallel()
	cat, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	p, _ := cat.PackByID("aws-cli")
	if anyPackRuleMatches(p, FixtureCase{Command: "aws --endpoint-url=http://localhost:4566 s3 rb s3://b"}) {
		t.Fatal("a declared loopback endpoint should be filtered")
	}
	if !anyPackRuleMatches(p, FixtureCase{Command: "aws s3 rb s3://b"}) {
		t.Fatal("expected s3-remove-bucket match")
	}
}

// An inert level means a rule can never ask, so a shipped rule that only asks
// would be dead weight. A mint rule is the exception: it raises no card and
// stays inert at every posture, because it marks evidence rather than risk.
func TestBreadthNoInertLevels(t *testing.T) {
	t.Parallel()
	cat, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	for _, p := range cat.Packs {
		for _, r := range p.Rules {
			mint := EffectFromTags(r.Tags).MintsCredential
			if mint && r.Level != LevelInformational {
				t.Errorf("%s/%s is a mint rule at level=%s; it must stay inert so it never asks",
					p.ID, r.Slug, r.Level)
				continue
			}
			if !mint && (r.Level == LevelLow || r.Level == LevelInformational) {
				t.Errorf("%s/%s level=%s", p.ID, r.Slug, r.Level)
			}
		}
	}
}

// Settings promises Light gets critical, Balanced adds high, and Strict adds
// medium. A band with no bundled rule is a posture the user can select and
// receive nothing for.
func TestBundledPacksPopulateEveryEscalatingBand(t *testing.T) {
	t.Parallel()
	cat, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	counts := map[Level]int{}
	for _, p := range cat.Packs {
		if p.Source != SourceBundled {
			continue
		}
		for _, r := range p.Rules {
			counts[r.Level]++
		}
	}
	for _, level := range []Level{LevelMedium, LevelHigh, LevelCritical} {
		if counts[level] == 0 {
			t.Errorf("no bundled rule at level %s", level)
		}
	}
}

func TestOverlayMediumStillEscalatesAtStrict(t *testing.T) {
	t.Parallel()
	body := `title: Overlay medium
id: 77777777-7777-4777-8777-777777777777
description: device overlay medium rule
logsource: {product: lycaon, service: tool_exec}
level: medium
detection:
  sel: {CommandLine|contains: ' overlay-medium-hit '}
  condition: sel`
	r, err := ParseRule([]byte(body))
	testutil.FailErr(t, "ParseRule", err)
	if !r.Supported {
		t.Fatalf("unsupported: %s", r.UnsupportedReason)
	}
	if !Escalates(LevelMedium, "strict") {
		t.Fatal("medium must escalate at strict")
	}
	if Escalates(LevelMedium, "balanced") {
		t.Fatal("medium must not escalate at balanced")
	}
	cat := &Catalog{Packs: []Pack{{
		ID: "overlay-medium", Enabled: true, Source: SourceDevice,
		Rules: []Rule{r},
	}}}
	m := NewMatcher(cat)
	hit, ok := m.Match(testEvent("command", "do overlay-medium-hit now", "/p", true, "proxy", "s"))
	if !ok || hit.Level != LevelMedium {
		t.Fatalf("hit=%+v ok=%v", hit, ok)
	}
}

func TestEmptyMatcherPreservesSilence(t *testing.T) {
	t.Parallel()
	m := NewMatcher(&Catalog{})
	if hit, ok := m.Match(testEvent("command", "aws organizations leave-organization", "/p", true, "proxy", "s")); ok {
		t.Fatalf("Match returned hit on empty catalog: %+v", hit)
	}
	if hit, ok := m.MatchEgress(fixtureEgressEvent("169.254.169.254")); ok {
		t.Fatalf("MatchEgress returned hit on empty catalog: %+v", hit)
	}
}

func fixtureEgressEvent(host string) EgressEvent {
	return NewEgressEvent(EgressObservation{
		DestinationHostname: host,
		DestinationPort:     443,
		Transport:           "http_connect",
		SessionID:           "s",
		ActionID:            "fixture-action",
		Origin:              "confined_proxy",
		DecisionStage:       "pre_dial",
	})
}
