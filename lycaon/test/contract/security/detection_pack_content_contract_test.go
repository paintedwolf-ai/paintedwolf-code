package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"gopkg.in/yaml.v3"
)

// TestDetectionPackAllBundledRulesSupported pins Supported==true and zero catalog warnings.
func TestDetectionPackAllBundledRulesSupported(t *testing.T) {
	t.Parallel()
	cat := loadBundledDetectionCatalog(t)
	if len(cat.Warnings) != 0 {
		t.Fatalf("catalog warnings: %v", cat.Warnings)
	}
	if len(cat.Packs) == 0 {
		t.Fatal("no packs loaded")
	}
	for _, p := range cat.Packs {
		for _, r := range p.Rules {
			if !r.Supported {
				t.Errorf("%s/%s unsupported: %s", p.ID, r.Slug, r.UnsupportedReason)
			}
		}
	}
}

// TestDetectionPackProviderPacksStayPointPrecise: each provider rule covers one
// verb axis, so no rule can quietly become a catch-all.
func TestDetectionPackProviderPacksStayPointPrecise(t *testing.T) {
	t.Parallel()
	for _, packID := range []string{"aws-cli", "azure-cli", "gcloud-cli"} {
		dir := config.DetectionPacksDir.Join(packID, "rules")
		ents, err := config.List(dir)
		testutil.FailErr(t, "ReadDir "+packID, err)
		verbUse := map[string]int{} // selection name → rule count
		for _, ent := range ents {
			name := ent.Name()
			if !strings.HasSuffix(name, ".yml") {
				continue
			}
			raw := readRuleYAML(t, dir.Join(name))
			det, _ := raw["detection"].(map[string]any)
			for k := range det {
				if strings.HasPrefix(k, "verbs_") {
					verbUse[k]++
				}
			}
		}
		for sel, n := range verbUse {
			if n >= 2 {
				t.Errorf("%s: shared verbs_* axis %s reused by %d rules", packID, sel, n)
			}
		}
	}
}

// TestDetectionPackGenerationCheckIsGated verifies the aggregate runs the codegen check.
func TestDetectionPackGenerationCheckIsGated(t *testing.T) {
	t.Parallel()
	doc := loadTaskfile(t)
	if _, ok := doc.Tasks["codegen:detection-packs:check"]; !ok {
		t.Fatal("Taskfile missing codegen:detection-packs:check target")
	}
	if !doc.reaches("check", "codegen:detection-packs:check") {
		t.Fatal("check does not reach codegen:detection-packs:check")
	}
}

// TestDetectionPackBundledPackAskOnly: Evaluate against a bundled GateSource.
func TestDetectionPackBundledPackAskOnly(t *testing.T) {
	cat := loadBundledDetectionCatalog(t)
	src := detectionpack.NewGateSource(detectionpack.NewMatcher(cat))
	approvalGate := gateWithDetection(t, gate.PostureBalanced, src)

	res, err := approvalGate.Evaluate(context.Background(), containedCommand("aws organizations leave-organization"))
	testutil.FailErr(t, "Evaluate", err)
	if res.Denied {
		t.Fatalf("bundled pack must not deny: %+v", res)
	}
	if res.Required() && res.AutoApproved() {
		t.Fatalf("Required+AutoApproved: %+v", res)
	}

	res, err = approvalGate.Evaluate(context.Background(), hitl.ProposedAction{
		Tool: "write", Files: []string{"/etc/hosts"}, ProjectDir: t.TempDir(), SessionID: "s1",
	})
	testutil.FailErr(t, "Evaluate deny", err)
	if !res.Required() || res.Gate() != api.GateOutsideRootsWrite {
		t.Fatalf("native path escape must ask outside_roots (not a detection card): %+v", res)
	}
	if res.DetectionCitation != nil {
		t.Fatalf("path escape must not cite a detection: %+v", res.DetectionCitation)
	}
}

// TestDetectionPackNoInertAuthoredLevels permits inert credential-minting evidence only.
func TestDetectionPackNoInertAuthoredLevels(t *testing.T) {
	t.Parallel()
	cat := loadBundledDetectionCatalog(t)
	for _, p := range cat.Packs {
		if p.Source != detectionpack.SourceBundled && p.Source != detectionpack.SourceGenerated {
			continue
		}
		for _, r := range p.Rules {
			mint := detectionpack.EffectFromTags(r.Tags).MintsCredential
			if mint && r.Level != detectionpack.LevelInformational {
				t.Errorf("%s/%s mint level=%s; want informational", p.ID, r.Slug, r.Level)
				continue
			}
			if !mint && (r.Level == detectionpack.LevelLow || r.Level == detectionpack.LevelInformational) {
				t.Errorf("%s/%s level=%s", p.ID, r.Slug, r.Level)
			}
		}
	}
}

// TestDetectionPackRulesAreGateShaped requires one card-reaching effect class.
func TestDetectionPackRulesAreGateShaped(t *testing.T) {
	t.Parallel()
	cat := loadBundledDetectionCatalog(t)
	for _, p := range cat.Packs {
		for _, r := range p.Rules {
			effect := detectionpack.EffectFromTags(r.Tags)
			if !effect.Tagged {
				continue
			}
			if effect.MintsCredential {
				continue
			}
			if effect.External && effect.Local {
				t.Errorf("%s/%s declares both %s and %s; a card can only state one place the effect lands",
					p.ID, r.Slug, detectionpack.TagEffectExternal, detectionpack.TagEffectLocal)
				continue
			}
			if effect.External || effect.Local {
				continue
			}
			t.Errorf("%s/%s declares an effect class without %s or %s, so it can never raise a card",
				p.ID, r.Slug, detectionpack.TagEffectExternal, detectionpack.TagEffectLocal)
		}
	}
}

// TestDetectionPackLevelMatchesEffectClass keeps severity aligned with recoverability.
func TestDetectionPackLevelMatchesEffectClass(t *testing.T) {
	t.Parallel()
	cat := loadBundledDetectionCatalog(t)
	for _, p := range cat.Packs {
		for _, r := range p.Rules {
			effect := detectionpack.EffectFromTags(r.Tags)
			if !effect.Tagged {
				continue
			}
			unrecoverable := effect.Unrecoverable
			switch r.Level {
			case detectionpack.LevelCritical, detectionpack.LevelHigh:
				if !unrecoverable {
					t.Errorf("%s/%s is %s but declares no %s", p.ID, r.Slug, r.Level,
						detectionpack.TagEffectUnrecoverable)
				}
			case detectionpack.LevelMedium:
				if unrecoverable {
					t.Errorf("%s/%s is medium but declares %s: an unrecoverable operation "+
						"belongs at high or critical", p.ID, r.Slug, detectionpack.TagEffectUnrecoverable)
				}
			case detectionpack.LevelInformational, detectionpack.LevelLow:
			}
		}
	}
}

// TestDetectionPackGeneratedCloudPacksPreserveReviewedPolicy checks parity.
func TestDetectionPackGeneratedCloudPacksPreserveReviewedPolicy(t *testing.T) {
	t.Parallel()
	for _, retired := range []string{"aws-sensitive-actions", "aws-structured-actions"} {
		if config.Has(config.DetectionPacksDir.Join(retired)) {
			t.Fatalf("retired %s pack must remain absent", retired)
		}
	}
	cat := loadBundledDetectionCatalog(t)
	for _, retired := range []string{"aws-sensitive-actions", "aws-structured-actions"} {
		if _, ok := cat.PackByID(retired); ok {
			t.Fatalf("catalog must not load retired %s", retired)
		}
	}
	for _, id := range []string{"gcp-structured-actions", "azure-structured-actions"} {
		pack, present := cat.PackByID(id)
		if !present || pack.Source != detectionpack.SourceGenerated {
			t.Errorf("%s = %+v, present=%v; want generated pack", id, pack, present)
		}
	}
	m := detectionpack.NewMatcher(cat)
	if hit, ok := m.Match(contractToolEvent("command", "aws s3api get-object --bucket b --key k", "/p", true, "proxy", "s")); ok {
		t.Fatalf("get-object must stay silent, got %+v", hit)
	}
	structured := detectionpack.NewEvent(detectionpack.ActionObservation{
		Tool:        "mcp_aws_call",
		ToolArgs:    []string{"action=iam:CreateAccessKey"},
		APIActions:  detectionpack.APIActionsFromStructuredArgs(map[string]any{"action": "iam:CreateAccessKey"}),
		EffectReach: detectionpack.EffectReachRemote,
	})
	if hit, matched := m.Match(structured); !matched || hit.PackID != "aws-cli" {
		t.Fatalf("exact CreateAccessKey match = %+v, %v; want aws-cli", hit, matched)
	}
	for _, tc := range []struct {
		tool, action, pack string
	}{
		{"mcp_gcp_call", "resourcemanager.projects.delete", "gcp-structured-actions"},
		{"mcp_azure_call", "Microsoft.Resources/subscriptions/resourceGroups/delete", "azure-structured-actions"},
	} {
		event := detectionpack.NewEvent(detectionpack.ActionObservation{
			Tool: tc.tool, ToolArgs: []string{"action=" + tc.action}, EffectReach: detectionpack.EffectReachRemote,
		})
		if hit, matched := m.Match(event); !matched || hit.PackID != tc.pack {
			t.Errorf("%s exact match = %+v, %v; want %s", tc.action, hit, matched, tc.pack)
		}
	}
}

// TestDetectionPackLogSourceFieldSetsClosed: no field outside closed set; egress never in Match.
func TestDetectionPackLogSourceFieldSetsClosed(t *testing.T) {
	t.Parallel()
	// The closed sets are the matcher's own, not a copy.
	toolExecFields := fieldSet(detectionpack.SupportedFields(detectionpack.SourceToolExec))
	egressFields := fieldSet(detectionpack.SupportedFields(detectionpack.SourceEgressObserved))
	cat := loadBundledDetectionCatalog(t)
	for _, p := range cat.Packs {
		for _, r := range p.Rules {
			raw := readRuleYAML(t, config.DetectionPacksDir.Join(p.ID, "rules", r.Slug+".yml"))
			ls, _ := raw["logsource"].(map[string]any)
			svc, _ := ls["service"].(string)
			allowed := toolExecFields
			if svc == "egress_observed" {
				allowed = egressFields
			}
			det, _ := raw["detection"].(map[string]any)
			for selName, selVal := range det {
				if selName == "condition" {
					continue
				}
				sel, ok := selVal.(map[string]any)
				if !ok {
					continue
				}
				for field := range sel {
					base := strings.Split(field, "|")[0]
					if _, ok := allowed[base]; !ok {
						t.Errorf("%s/%s references field %q outside %s set", p.ID, r.Slug, base, svc)
					}
				}
			}
		}
	}
	m := detectionpack.NewMatcher(cat)
	if hit, ok := m.Match(contractToolEvent("command", "curl https://169.254.169.254", "/p", true, "proxy", "s")); ok {
		if hit.PackID == "egress-providers" {
			t.Fatalf("Match returned egress rule: %+v", hit)
		}
	}
}

// TestDetectionPackLauncherNamesNotInImageLists: launcher set absent except secrets-identity verbs.
func TestDetectionPackLauncherNamesNotInImageLists(t *testing.T) {
	t.Parallel()
	launchers := map[string]bool{
		"npx": true, "bunx": true, "aws-vault": true, "sudo": true, "env": true,
	}
	// Credential brokers are peeled to the wrapped tool, so naming one as an
	// Image would match a launcher rather than the operation.
	secretsOK := map[string]bool{"doppler": true, "op": true}
	root := detectionPackConfigRoot(t)
	cat := loadBundledDetectionCatalog(t)
	for _, p := range cat.Packs {
		for _, r := range p.Rules {
			images := ruleImageEqualsFromDisk(t, root, p.ID, r.Slug)
			for _, img := range images {
				if launchers[img] {
					t.Errorf("%s/%s Image lists launcher %q", p.ID, r.Slug, img)
				}
				if secretsOK[img] {
					t.Errorf("%s/%s Image lists %q", p.ID, r.Slug, img)
				}
			}
		}
	}
}

// TestDetectionPackUniqueIDsValidLevelsFixturesLogsource pins identity / fixtures / logsource / prose.
func TestDetectionPackUniqueIDsValidLevelsFixturesLogsource(t *testing.T) {
	t.Parallel()
	cat := loadBundledDetectionCatalog(t)
	seen := map[string]string{}
	validLevels := map[detectionpack.Level]bool{
		detectionpack.LevelInformational: true,
		detectionpack.LevelLow:           true,
		detectionpack.LevelMedium:        true,
		detectionpack.LevelHigh:          true,
		detectionpack.LevelCritical:      true,
	}
	for _, p := range cat.Packs {
		escalating := false
		for _, r := range p.Rules {
			if _, err := uuid.Parse(r.ID); err != nil {
				t.Errorf("%s/%s id not UUID: %s", p.ID, r.Slug, r.ID)
			}
			if prev, ok := seen[r.ID]; ok {
				t.Errorf("duplicate rule id %s in %s and %s/%s", r.ID, prev, p.ID, r.Slug)
			}
			seen[r.ID] = p.ID + "/" + r.Slug
			if !validLevels[r.Level] {
				t.Errorf("%s/%s invalid level %s", p.ID, r.Slug, r.Level)
			}
			switch r.Level {
			case detectionpack.LevelMedium, detectionpack.LevelHigh, detectionpack.LevelCritical:
				escalating = true
			case detectionpack.LevelInformational, detectionpack.LevelLow:
			}
			raw := readRuleYAML(t, config.DetectionPacksDir.Join(p.ID, "rules", r.Slug+".yml"))
			ls, _ := raw["logsource"].(map[string]any)
			product, _ := ls["product"].(string)
			svc, _ := ls["service"].(string)
			if product != "lycaon" {
				t.Errorf("%s/%s product=%q", p.ID, r.Slug, product)
			}
			if p.ID == "egress-providers" {
				if svc != "egress_observed" || r.Source != detectionpack.SourceEgressObserved {
					t.Errorf("%s/%s must be egress_observed, got %q / %s", p.ID, r.Slug, svc, r.Source)
				}
			} else if svc != "tool_exec" || r.Source != detectionpack.SourceToolExec {
				t.Errorf("%s/%s must be tool_exec, got %q / %s", p.ID, r.Slug, svc, r.Source)
			}
		}
		if !escalating && p.ID != "credential-minting" {
			t.Errorf("pack %s has no medium+ rule for Strict escalation", p.ID)
		}

		fxPath := config.DetectionPacksDir.Join(p.ID, "fixtures.yaml")
		data, err := config.Read(fxPath)
		testutil.FailErr(t, "read fixtures "+p.ID, err)
		var fx struct {
			Fixtures []struct {
				Rule     string                      `yaml:"rule"`
				Positive []detectionpack.FixtureCase `yaml:"positive"`
				Negative []detectionpack.FixtureCase `yaml:"negative"`
			} `yaml:"fixtures"`
		}
		testutil.FailErr(t, "parse fixtures "+p.ID, yaml.Unmarshal(data, &fx))
		have := map[string]struct {
			pos, neg int
		}{}
		for _, row := range fx.Fixtures {
			have[row.Rule] = struct{ pos, neg int }{len(row.Positive), len(row.Negative)}
		}
		for _, r := range p.Rules {
			row, ok := have[r.Slug]
			if !ok || row.pos < 1 || row.neg < 1 {
				t.Errorf("%s/%s needs ≥1 positive and ≥1 negative fixture", p.ID, r.Slug)
			}
		}
	}

	// Corpus smoke: one positive per pack still matches through its production source.
	semantics, err := detectionpack.LoadActionSemantics(t.TempDir())
	testutil.FailErr(t, "load action semantics", err)
	for _, p := range cat.Packs {
		fxPath := config.DetectionPacksDir.Join(p.ID, "fixtures.yaml")
		data, err := config.Read(fxPath)
		testutil.FailErr(t, "read fixtures", err)
		var fx struct {
			Fixtures []struct {
				Rule     string                      `yaml:"rule"`
				Positive []detectionpack.FixtureCase `yaml:"positive"`
			} `yaml:"fixtures"`
		}
		testutil.FailErr(t, "parse", yaml.Unmarshal(data, &fx))
		if len(fx.Fixtures) == 0 || len(fx.Fixtures[0].Positive) == 0 {
			continue
		}
		pos := fx.Fixtures[0].Positive[0]
		ruleSlug := fx.Fixtures[0].Rule
		var rule detectionpack.Rule
		for _, r := range p.Rules {
			if r.Slug == ruleSlug {
				rule = r
				break
			}
		}
		if rule.Source == detectionpack.SourceEgressObserved {
			if !rule.MatchesEgress(contractEgressEvent(pos.Command)) {
				t.Errorf("%s/%s positive %q did not match", p.ID, ruleSlug, pos.Command)
			}
		} else if !contractFixtureMatchesRule(p, rule, pos, semantics) {
			t.Errorf("%s/%s positive %q did not match", p.ID, ruleSlug, pos.Command)
		}
	}
}
