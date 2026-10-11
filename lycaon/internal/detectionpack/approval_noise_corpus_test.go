package detectionpack

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

type approvalNoiseCorpus struct {
	Version int                       `yaml:"version"`
	Cases   []approvalNoiseCorpusCase `yaml:"cases"`
}

type approvalNoiseCorpusCase struct {
	ID               string         `yaml:"id"`
	Posture          gate.Posture   `yaml:"posture"`
	Tool             string         `yaml:"tool"`
	Args             map[string]any `yaml:"args"`
	TargetFiles      []string       `yaml:"target_files"`
	ApprovalCategory string         `yaml:"approval_category"`
	ApprovalSubject  string         `yaml:"approval_subject"`
	Want             string         `yaml:"want"`
}

func TestApprovalNoiseCorpusThroughProductionAdapter(t *testing.T) {
	data, err := config.Read(config.DetectionApprovalCorpus)
	testutil.FailErr(t, "read approval noise corpus", err)
	var corpus approvalNoiseCorpus
	testutil.FailErr(t, "parse approval noise corpus", yaml.Unmarshal(data, &corpus))
	if corpus.Version != 1 || len(corpus.Cases) < 20 {
		t.Fatalf("corpus version=%d cases=%d", corpus.Version, len(corpus.Cases))
	}

	catalog, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "load detection catalog", err)
	semantics, err := LoadActionSemantics("")
	testutil.FailErr(t, "load action semantics", err)
	source := NewGateSource(NewMatcher(catalog), semantics)
	seen := map[string]struct{}{}
	for _, tc := range corpus.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			if strings.TrimSpace(tc.ID) == "" || strings.TrimSpace(tc.Tool) == "" || strings.TrimSpace(tc.Want) == "" {
				t.Fatalf("incomplete case: %+v", tc)
			}
			if _, duplicate := seen[tc.ID]; duplicate {
				t.Fatalf("duplicate id %q", tc.ID)
			}
			seen[tc.ID] = struct{}{}
			hit, matched := source.MatchAction(hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: tc.Tool,
Args: tc.Args,
Files: tc.TargetFiles,
ActionID: tc.ID,
},
Resources: hitl.ActionResources{
ApprovalCategory: tc.ApprovalCategory,
ApprovalSubject: tc.ApprovalSubject,
},
Scope: hitl.ActionScope{
ProjectDir: "/project",
SessionID: "noise-corpus",
},
Execution: hitl.ActionExecution{
Contained: hitl.Contained{
					FSJailed: true, Egress: hitl.ContainedEgressProxy,
					Roots: []string{"/project"}, WriteRoots: []string{"/project"},
				},
},
}, tc.Posture)
			if tc.Want == "silent" {
				if matched {
					t.Fatalf("unexpected %s/%s (%s)", hit.PackID, hit.RuleTitle, hit.Level)
				}
				return
			}
			packID, slug, ok := strings.Cut(tc.Want, "/")
			if !ok {
				t.Fatalf("want=%q must be silent or pack/slug", tc.Want)
			}
			wantRule := corpusRuleBySlug(t, catalog, packID, slug)
			if !matched || hit.PackID != packID || hit.RuleID != wantRule.ID {
				t.Fatalf("match=%v hit=%s/%s (%s), want %s", matched, hit.PackID, hit.RuleTitle, hit.Level, tc.Want)
			}
		})
	}
}

func corpusRuleBySlug(t *testing.T, catalog *Catalog, packID, slug string) Rule {
	t.Helper()
	pack, ok := catalog.PackByID(packID)
	if !ok {
		t.Fatalf("unknown pack %q", packID)
	}
	for _, rule := range pack.Rules {
		if rule.Slug == slug {
			return rule
		}
	}
	t.Fatalf("unknown rule %s/%s", packID, slug)
	return Rule{}
}
