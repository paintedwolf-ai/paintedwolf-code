package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// hostVarProducers pairs each SetHostVar key with a regex proving a live
// reader in non-test Go source under internal/.
var hostVarProducers = []struct {
	key       string // SetHostVar key literal (or const value)
	readerRE  string // regex proving a Go reader exists somewhere under internal/
	rationale string // one-line description of what this key means
}{
	{
		key:       "fanout_coverage",
		readerRE:  `vars\["fanout_coverage"\]`,
		rationale: "coordinator execution snapshot includes recorded attempts",
	},
	{
		key:       "fanout_settled",
		readerRE:  `BoolVar\(ec\.Vars, "fanout_settled"\)`,
		rationale: "worker-cycle gate requires every planned leg to settle",
	},
	{
		key:       "artifact.plan.breaking",
		readerRE:  `"artifact\.plan\.breaking"`,
		rationale: "plan artifact frontmatter for inject",
	},
	{
		key:       "artifact.plan.scope",
		readerRE:  `"artifact\.plan\.scope"`,
		rationale: "plan artifact frontmatter for inject",
	},
	{
		key:       "artifact.selection.criterion",
		readerRE:  `"artifact\.selection\.criterion"`,
		rationale: "options selection report frontmatter from topology",
	},
	{
		key:       "options.criterion",
		readerRE:  `"options\.criterion"`,
		rationale: "design-fork criterion for debate-select kick",
	},
	{
		key:       "last_failed_leaves",
		readerRE:  `hostStringSliceVar\([^,]+, "last_failed_leaves"\)`,
		rationale: "coordinator run context surfaces gate-blocked failed leaves",
	},
	{
		key:       "workflow_compose_summary_id",
		readerRE:  `hostStringVar\([^,]+, "workflow_compose_summary_id"\)`,
		rationale: "coordinator context loader looks up composed manifest summary",
	},
	{
		key:       "host_auto_advanced_from",
		readerRE:  `vars\[HostAutoAdvancedFromKey\]`,
		rationale: "marker consumed by workflow_advance tool already_advanced response",
	},
	{
		key:       "pre_workflow_posture",
		readerRE:  `vars\[hostVarBaselinePosture\]`,
		rationale: "boundary code restores posture on workflow end",
	},
	{
		key:       "topology_outputs.",
		readerRE:  `"topology_outputs"`,
		rationale: "fan_out/pack merged output for topology-synthesis coordinator kick",
	},
	{
		key:       "evidence_digest",
		readerRE:  `"evidence_digest"`,
		rationale: "curated worker-union digest for synthesis kicks and closeout inject",
	},
	{
		key:       "human_approval.active",
		readerRE:  `DotPathTruthy\([^,]+, "human_approval\.active"\)`,
		rationale: "human_approval awaiting gate",
	},
	{
		key:       "human_approval.awaiting_since",
		readerRE:  `scaffoldvars\.HumanApprovalAwaitingSince\(`,
		rationale: "attention ages a plan awaiting approval from when the wait opened",
	},
	{
		key:       "human_approval.blueprint_hash",
		readerRE:  `"human_approval\.blueprint_hash"`,
		rationale: "human_approval content-hash gate",
	},
	{
		key:       "human_approval.blueprint_path",
		readerRE:  `"human_approval\.blueprint_path"`,
		rationale: "human_approval blueprint reader",
	},
	{
		key:       "human_approval.issued",
		readerRE:  `DotPathTruthy\([^,]+, "human_approval\.issued"\)`,
		rationale: "human_approval gate condition",
	},
	{
		key:       "human_approval.ready",
		readerRE:  `DotPathTruthy\([^,]+, "human_approval\.ready"\)`,
		rationale: "human_approval drop-early gate",
	},
	{
		key:       "intake.",
		readerRE:  `"intake\."`,
		rationale: "intake gate mapped values for grounding",
	},
	{
		key:       "params.",
		readerRE:  `"params\."`,
		rationale: "workflow depth parameters for phase scaling",
	},
	{
		key:       "review_if_spawnable.",
		readerRE:  `"review_if_spawnable"`,
		rationale: "phase-enter snapshot of if_spawnable reviewers that the turn roster can spawn",
	},
	{
		key:       "review_verdict.",
		readerRE:  `"review_verdict"`,
		rationale: "terminal review_loop verdict stamped for later kicks",
	},
	{
		key:       "artifact.",
		readerRE:  `"artifact\."`,
		rationale: "blueprint frontmatter depth fields for inject (artifact.<id>.* keep)",
	},
	{
		key:       "phase_skipped.",
		readerRE:  `"phase_skipped\."`,
		rationale: "dynamic phase resolution flags from depth parameters",
	},
	{
		key:       "phase_skipped.approve",
		readerRE:  `"phase_skipped\.approve"`,
		rationale: "plan@ auto_approve param skips approve",
	},
	{
		key:       "workflow.preset_id",
		readerRE:  `"workflow\.preset_id"`,
		rationale: "coordinator context surfaces active preset",
	},
}

func TestHostVarProducerConsumerClosure(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	internalDir := filepath.Join(root, "lycaon", "internal")

	// Keys produced through a named const rather than a SetHostVar literal.
	constProduced := map[string]bool{
		"host_auto_advanced_from":       true, // HostAutoAdvancedFromKey
		"pre_workflow_posture":          true, // hostVarBaselinePosture
		"human_approval.awaiting_since": true, // scaffoldvars.HumanApprovalAwaitingSincePath
	}

	// Every ledger key has a live reader.
	for _, entry := range hostVarProducers {
		t.Run("reader/"+entry.key, func(t *testing.T) {
			t.Parallel()
			re, err := regexp.Compile(entry.readerRE)
			if err != nil {
				t.Fatalf("compile readerRE for %q: %v", entry.key, err)
			}
			if found := grepGoSource(t, internalDir, re); !found {
				t.Fatalf("host var %q has no reader matching %s — zombie write?\nEither delete the SetHostVar call or wire a consumer.\nRationale: %s",
					entry.key, entry.readerRE, entry.rationale)
			}
		})
	}

	// Every SetHostVar literal key is in the ledger.
	literals := findHostVarLiterals(t, internalDir)
	declared := map[string]bool{}
	for _, e := range hostVarProducers {
		declared[e.key] = true
	}

	var orphans []string
	for k := range literals {
		if !declared[k] {
			orphans = append(orphans, k)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Errorf("SetHostVar producers not in hostVarProducers ledger: %v\n"+
			"Add each with a reader regex proving a live consumer, or delete the orphan write.", orphans)
	}

	// Every ledger key is produced.
	var unproduced []string
	for _, e := range hostVarProducers {
		if !literals[e.key] && !constProduced[e.key] {
			unproduced = append(unproduced, e.key)
		}
	}
	sort.Strings(unproduced)
	if len(unproduced) > 0 {
		t.Errorf("hostVarProducers ledger lists keys no SetHostVar call produces: %v", unproduced)
	}
}

// grepGoSource walks dir for .go files (skipping _test.go) and reports
// whether the regex matches any file's contents.
func grepGoSource(t *testing.T, dir string, re *regexp.Regexp) bool {
	t.Helper()
	found := false
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if re.Match(data) {
			found = true
		}
		return nil
	})
	contractcheck.FailErr(t, "operation failed", err)
	return found
}

// findHostVarLiterals scans non-test Go source for SetHostVar(..., "<key>", ...)
// call sites and returns the set of literal keys.
func findHostVarLiterals(t *testing.T, dir string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	re := regexp.MustCompile(`SetHostVar\([^,]+,\s*"([a-zA-Z_][a-zA-Z0-9_.]*)"`)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range re.FindAllStringSubmatch(string(data), -1) {
			out[m[1]] = true
		}
		return nil
	})
	contractcheck.FailErr(t, "operation failed", err)
	return out
}
