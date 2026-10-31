package llm

import (
	"context"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
)

// GlossLine is a capped navigation hint with no handle and no groundable claim.
type GlossLine struct {
	Label string `json:"label"`
}

// MaterializedSelection is one resolve-or-drop selection with verbatim materialized lines.
type MaterializedSelection struct {
	Triple     evidence.Triple     `json:"triple"`
	Resolution evidence.Resolution `json:"resolution"`
	Lines      []string            `json:"lines,omitempty"`
}

// CurationReport describes curator coverage and operational flags.
type CurationReport struct {
	Selected int  `json:"selected"`
	Total    int  `json:"total"`
	Dropped  int  `json:"dropped"`
	Retries  int  `json:"retries"`
	CacheHit bool `json:"cache_hit,omitempty"`
	Fallback bool `json:"fallback,omitempty"`
}

// CurationResult is the curator output: resolved verbatim selections + fenced gloss.
type CurationResult struct {
	Selections []MaterializedSelection `json:"selections,omitempty"`
	Gloss      []GlossLine             `json:"gloss,omitempty"`
	Report     CurationReport          `json:"report"`
}

// Curator selects verbatim evidence references from a candidate snapshot ledger.
// snapshot holds only the current tool's deterministic output, not the
// accumulated session ledger, so prior survey handles cannot be selected.
type Curator interface {
	Curate(ctx context.Context, snapshot evidence.Ledger, focus CurationFocus, budget int) (CurationResult, error)
}

const (
	defaultCurationBudget = 5
	maxGlossLines         = 5
	maxGlossLabelRunes    = 80
	curatorMaxRetries     = 1
)

// NewTestRegistrySummarizer wires a stub lite provider for contract and integration tests.
func NewTestRegistrySummarizer(provider modelcall.Provider) (*RegistrySummarizer, error) {
	dir, err := os.MkdirTemp("", "curator-test-policy")
	if err != nil {
		return nil, err
	}
	policyPath := filepath.Join(dir, "model-policy.yaml")
	data, err := yaml.Marshal(ModelPolicy{
		Lite: ModelRef{ProviderID: provider.ID(), Model: "lite"},
	})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(policyPath, data, 0o600); err != nil {
		return nil, err
	}
	policy, err := NewPolicyStoreAt(policyPath)
	if err != nil {
		return nil, err
	}
	reg := newEmptyRegistry()
	reg.SetOutboundSecretScreen(NewModelSecretScreen(nil, nil))
	if err := reg.Register(provider); err != nil {
		return nil, err
	}
	return &RegistrySummarizer{
		Registry: reg,
		Policy:   policy,
		Scope:    SettingsScopeGlobal,
	}, nil
}

// CurationFocus orients the lite curator model. CacheKey excludes Task so identical
// snapshots reuse curation; PromptFocus includes Task when present.
type CurationFocus struct {
	Tool   string
	View   string
	Target string
	Task   string
}

// CacheKey fingerprints tool/view/target for curation cache lookup.
func (f CurationFocus) CacheKey() string {
	return strings.Join([]string{
		strings.TrimSpace(f.Tool),
		strings.TrimSpace(f.View),
		strings.TrimSpace(f.Target),
	}, "|")
}

// PromptFocus is the focus line rendered into the curator user prompt.
func (f CurationFocus) PromptFocus() string {
	var head strings.Builder
	if tool := strings.TrimSpace(f.Tool); tool != "" {
		head.WriteString(tool)
	}
	if view := strings.TrimSpace(f.View); view != "" {
		if head.Len() > 0 {
			head.WriteByte(' ')
		}
		head.WriteString(view)
	}
	if target := strings.TrimSpace(f.Target); target != "" {
		if head.Len() > 0 {
			head.WriteString(": ")
		}
		head.WriteString(target)
	}
	out := head.String()
	if task := strings.TrimSpace(f.Task); task != "" {
		if out != "" {
			out += "\n"
		}
		out += "Task: " + task
	}
	return out
}

func SearchFocus(ctx context.Context, tool, target string) CurationFocus {
	return curationFocus(ctx, tool, "matches", target)
}

func curationFocus(ctx context.Context, tool, view, target string) CurationFocus {
	return CurationFocus{
		Tool:   tool,
		View:   view,
		Target: target,
		Task:   curationctx.TaskHint(ctx),
	}
}
