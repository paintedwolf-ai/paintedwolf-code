package contract

import (
	"context"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Workflow primitives stay independent of a specific workflow.
func TestOptionsWorkflowUsesGenericPrimitives(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "options", "workflows", "options", "workflow.yaml")
	m, err := workflowdef.LoadManifestFromFile(path)
	contractcheck.FailErr(t, "LoadManifestFromFile options", err)

	var hasReviewLoop, hasHumanApproval bool
	for _, ph := range m.PhaseDefs {
		if ph.ReviewLoop != nil {
			hasReviewLoop = true
		}
		if ph.HumanApproval != nil {
			hasHumanApproval = true
		}
	}
	if m.Request == nil || m.Request.Question == "" {
		t.Fatal("options workflow missing request contract")
	}
	if !hasReviewLoop {
		t.Fatal("options workflow missing review_loop phase block")
	}
	if !hasHumanApproval {
		t.Fatal("options workflow missing human_approval gate")
	}

	// Plan-only manifest ids must not appear in options YAML.
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read options workflow", err)
	text := string(data)
	for _, forbidden := range []string{"plan_stub_valid", "research_satisfied", "plan-writer"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("options workflow leaks plan-only identifier %q", forbidden)
		}
	}
}

func TestHumanApprovalPrimitiveHasNonPlanConsumer(t *testing.T) {
	t.Parallel()
	resolver := workflowcatalog.Resolver{}
	summaries, err := resolver.ListResolved(context.Background(), "", "")
	contractcheck.FailErr(t, "ListResolved", err)

	catalog, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "loadMergedWorkflowCatalog", err)

	planUses := false
	optionsUses := false
	for id, m := range catalog {
		for _, ph := range m.PhaseDefs {
			if ph.HumanApproval == nil {
				continue
			}
			switch {
			case strings.HasPrefix(id, "plan@"):
				planUses = true
			case strings.HasPrefix(id, "options@"):
				optionsUses = true
			}
		}
	}
	_ = summaries
	if !planUses {
		t.Fatal("plan manifest must use human_approval gate")
	}
	if !optionsUses {
		t.Fatal("options manifest must use human_approval gate (generic primitive reuse)")
	}
}
