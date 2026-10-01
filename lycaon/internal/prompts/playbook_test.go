package prompts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func loadTestPlaybookMatcher(t *testing.T) *prompts.PlaybookMatcher {
	t.Helper()
	contract, err := prompts.LoadPersonaContract()
	testutil.FailErr(t, "prompts.LoadPersonaContract failed", err)
	m, err := prompts.LoadPlaybookMatcherEffective(contract)
	testutil.FailErr(t, "prompts.LoadPlaybookMatcherEffective failed", err)
	return m
}

func TestMatchForAgent(t *testing.T) {
	m := loadTestPlaybookMatcher(t)
	got, err := m.MatchForAgent("implementer", "pipeline", "implement")
	testutil.FailErr(t, "m.MatchForAgent failed", err)
	if len(got) == 0 {
		t.Fatal("expected checklist items")
	}
}

func TestMatchPlaybookByPhase(t *testing.T) {
	m := loadTestPlaybookMatcher(t)
	got, err := m.MatchPlaybook([]string{"worker-leg-default", "implement-addenda"}, "pipeline", "implement")
	testutil.FailErr(t, "m.MatchPlaybook failed", err)
	if !containsAll(got, "Run tests and the leg's external-state commands", "Branch on rejection Code:") {
		t.Fatalf("checklist = %v", got)
	}
}

func TestPlaybookFallback(t *testing.T) {
	m := loadTestPlaybookMatcher(t)
	got, err := m.MatchPlaybook([]string{"worker-leg-default", "implement-addenda"}, "pipeline", "unknown-phase-xyz")
	testutil.FailErr(t, "m.MatchPlaybook failed", err)
	if containsAny(got, "Run tests and the leg's external-state commands") {
		t.Fatalf("addenda leaked on unknown phase: %v", got)
	}
	if !containsAll(got, "Branch on rejection Code:", "Claim evidence_passed:* only from inspector JSONL anchors") {
		t.Fatalf("fallback missing: %v", got)
	}
}

func TestPlaybookExtendsMerge(t *testing.T) {
	m := loadTestPlaybookMatcher(t)
	got, err := m.MatchPlaybook([]string{"worker-leg-default", "implement-addenda"}, "pipeline", "implement")
	testutil.FailErr(t, "m.MatchPlaybook failed", err)
	idxBase := indexOf(got, "Branch on rejection Code:")
	idxAddenda := indexOf(got, "Run tests and the leg's external-state commands")
	if idxBase < 0 || idxAddenda < 0 || idxBase > idxAddenda {
		t.Fatalf("order wrong: %v", got)
	}
}

func TestPersonaContractWorkerAgents(t *testing.T) {
	contract, err := prompts.LoadPersonaContract()
	testutil.FailErr(t, "prompts.LoadPersonaContract failed", err)
	ids := contract.WorkerAgentIDs()
	if len(ids) == 0 {
		t.Fatal("expected worker agent ids")
	}
	if !contract.IsWorkerAgent("implementer") {
		t.Fatal("implementer should be worker")
	}
	if contract.IsWorkerAgent("coordinator") {
		t.Fatal("coordinator should not be worker")
	}
}

func TestPlaybookLoadRejectsDuplicateID(t *testing.T) {
	dir := t.TempDir()
	writePlaybook(t, dir, "a.yaml", "id: dup\nfallback: true\nchecklist: [x]\ntriggers:\n  topology_patterns: ['*']\n  phase_ids: ['*']\n")
	writePlaybook(t, dir, "b.yaml", "id: dup\nchecklist: [y]\ntriggers:\n  topology_patterns: ['*']\n  phase_ids: ['*']\n")
	_, err := prompts.LoadPlaybookMatcher(dir, nil)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err = %v", err)
	}
}

func writePlaybook(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
}

func containsAll(items []string, subs ...string) bool {
	joined := strings.Join(items, "\n")
	for _, s := range subs {
		if !strings.Contains(joined, s) {
			return false
		}
	}
	return true
}

func containsAny(items []string, sub string) bool {
	for _, item := range items {
		if strings.Contains(item, sub) {
			return true
		}
	}
	return false
}

func indexOf(items []string, sub string) int {
	for i, item := range items {
		if strings.Contains(item, sub) {
			return i
		}
	}
	return -1
}
