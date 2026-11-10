package contract

import (
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// The registry distinguishes model repair from host-attached coordinator citations.
func TestCitationGroundingRetryCodesMatchRegistry(t *testing.T) {
	t.Parallel()
	hintCfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	want := append([]string(nil), evidenceGroundingInSessionRetryCodes...)
	contractcheck.FailSetEqual(t, "registry InSessionRetryCodes", want, hintCfg.InSessionRetryCodes())
}

func TestMaxCitationGroundingRetries(t *testing.T) {
	t.Parallel()
	if got := compaction.DefaultCompactionConfig().MaxCitationGroundingRetries; got != limits.DefaultCitationGroundingRetries {
		t.Fatalf("MaxCitationGroundingRetries = %d, want %d", got, limits.DefaultCitationGroundingRetries)
	}
	// limits.DefaultCitationGroundingRetries is itself loaded from compaction.yaml,
	// so the bundled YAML is read directly to tie the retry ceiling to that file.
	raw, err := config.Read(config.Compaction)
	contractcheck.FailErr(t, "read bundled compaction.yaml", err)
	var doc struct {
		MaxCitationGroundingRetries int `yaml:"max_citation_grounding_retries"`
	}
	contractcheck.FailErr(t, "parse bundled compaction.yaml", yaml.Unmarshal(raw, &doc))
	if doc.MaxCitationGroundingRetries != limits.DefaultCitationGroundingRetries {
		t.Fatalf("compaction.yaml max_citation_grounding_retries = %d, want %d",
			doc.MaxCitationGroundingRetries, limits.DefaultCitationGroundingRetries)
	}
}
