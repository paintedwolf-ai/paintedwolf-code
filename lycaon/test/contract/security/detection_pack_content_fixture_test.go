package contract

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func detectionPackConfigRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join(contractcheck.RepoRoot(t), "lycaon")
}

func contractToolEvent(
	tool, command, projectDir string,
	contained bool,
	egressMode, sessionID string,
	targetFiles ...string,
) detectionpack.Event {
	return detectionpack.NewEvent(detectionpack.ActionObservation{
		Tool:        tool,
		CommandLine: command,
		ProjectDir:  projectDir,
		SessionID:   sessionID,
		Boundary:    hitl.Contained{FSJailed: contained, Egress: egressMode},
		// A rule may match the paths an action names rather than its argv; a
		// fixture that declares target_files is exercising exactly that.
		TargetFiles: targetFiles,
	})
}

var (
	bundledDetectionCatalogOnce sync.Once
	bundledDetectionCatalog     *detectionpack.Catalog
	bundledDetectionCatalogErr  error
)

func loadBundledDetectionCatalog(t *testing.T) *detectionpack.Catalog {
	t.Helper()
	bundledDetectionCatalogOnce.Do(func() {
		configDir := t.TempDir()
		packs, warnings, err := detectionpack.ShippedPacks()
		if err != nil {
			bundledDetectionCatalogErr = err
			return
		}
		if len(warnings) != 0 {
			bundledDetectionCatalogErr = fmt.Errorf("shipped detection warnings: %v", warnings)
			return
		}
		bundledDetectionCatalog, bundledDetectionCatalogErr = detectionpack.LoadCatalog(detectionpack.Input{
			ConfigDir: configDir, Contributed: packs,
		})
	})
	testutil.FailErr(t, "LoadCatalog", bundledDetectionCatalogErr)
	return bundledDetectionCatalog
}

func contractFixtureMatchesRule(
	pack detectionpack.Pack,
	rule detectionpack.Rule,
	fixture detectionpack.FixtureCase,
	semantics *detectionpack.ActionSemanticsCatalog,
) bool {
	tool := fixture.Tool
	if tool == "" {
		tool = "command"
	}
	args := fixture.Args
	if len(args) == 0 {
		args = map[string]any{"command": fixture.Command}
	}
	source := detectionpack.NewGateSource(detectionpack.NewMatcher(&detectionpack.Catalog{Packs: []detectionpack.Pack{{
		ID: pack.ID, Enabled: true, Rules: []detectionpack.Rule{rule},
	}}}), semantics)
	action := hitl.ProposedAction{
		Tool:             tool,
		Args:             args,
		Files:            fixture.TargetFiles,
		ApprovalCategory: fixture.ApprovalCategory,
		ApprovalSubject:  fixture.ApprovalSubject,
		ProjectDir:       "/tmp/proj",
		SessionID:        "s",
		ActionID:         "contract-fixture",
		Contained: hitl.Contained{
			FSJailed: true,
			Egress:   "proxy",
			Roots:    []string{"/tmp/proj"},
		},
	}
	if detectionpack.EffectFromTags(rule.Tags).MintsCredential {
		hit, ok := source.MintedCredentialRule(action)
		return ok && hit.RuleID == rule.ID
	}
	hit, ok := source.MatchAction(action, "strict")
	return ok && hit.RuleID == rule.ID
}

func contractEgressEvent(host string) detectionpack.EgressEvent {
	return detectionpack.NewEgressEvent(detectionpack.EgressObservation{
		DestinationHostname: host,
		DestinationPort:     443,
		Transport:           "http_connect",
		SessionID:           "s",
		ActionID:            "contract-action",
		Origin:              "confined_proxy",
		DecisionStage:       "pre_dial",
	})
}

type stubConfineEgressDetection struct {
	match confine.EgressDetectionCitation
	ok    bool
}

func (s stubConfineEgressDetection) Match(confine.EgressDetectionObservation) (confine.EgressDetectionCitation, bool) {
	return s.match, s.ok
}

func (s stubConfineEgressDetection) Escalates(match confine.EgressDetectionCitation, posture gate.Posture) bool {
	return detectionpack.Escalates(detectionpack.Level(match.Level), posture)
}

func mustParseDetectionRule(t *testing.T, body string) detectionpack.Rule {
	t.Helper()
	r, err := detectionpack.ParseRule([]byte(body))
	testutil.FailErr(t, "ParseRule", err)
	if !r.Supported {
		t.Fatalf("rule unsupported: %s", r.UnsupportedReason)
	}
	return r
}

func readRuleYAML(t *testing.T, rel config.Rel) map[string]any {
	t.Helper()
	data, err := config.Read(rel)
	testutil.FailErr(t, "read "+rel.String(), err)
	var raw map[string]any
	testutil.FailErr(t, "parse "+rel.String(), yaml.Unmarshal(data, &raw))
	return raw
}

func ruleImageEqualsFromDisk(t *testing.T, root, packID, slug string) []string {
	t.Helper()
	raw := readRuleYAML(t, config.DetectionPacksDir.Join(packID, "rules", slug+".yml"))
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
			if strings.Split(fk, "|")[0] != "Image" {
				continue
			}
			out = append(out, yamlStringList(fv)...)
		}
	}
	for i := range out {
		out[i] = strings.ToLower(out[i])
	}
	return out
}

func yamlStringList(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func fieldSet(fields []string) map[string]struct{} {
	out := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		out[field] = struct{}{}
	}
	return out
}
