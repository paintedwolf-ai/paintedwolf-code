package contract

import (
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

func TestBundledCatalogDemoSKUs(t *testing.T) {
	t.Parallel()
	catalog, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "loadMergedWorkflowCatalog", err)

	want := []string{
		"plan@1.0.1",
		"plan@1.0.0",
		"security-survey@2.0.1",
		"recon-pack@1.0.1",
		"recon-pack@1.0.0",
		"bugbash@1.1.0",
		"options@1.0.0",
		"implement@1.0.0",
	}
	for _, key := range want {
		if _, ok := catalog[key]; !ok {
			t.Fatalf("bundled catalog missing %q", key)
		}
	}
}
