package search

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func cf07G03Seed(base, family string, idx int) int64 {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", base, family, idx)))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

type cf07G03Summary struct {
	Family                 string `json:"family"`
	Lane                   string `json:"lane"`
	SeedBase               string `json:"seed_base"`
	TotalInstances         int    `json:"total_instances"`
	RegexUnsupportedPassed int    `json:"regex_unsupported_passed"`
	MatchBudgetPassed      int    `json:"match_budget_passed"`
	WorkStepsRefused       int    `json:"work_steps_refused"`
	PatternBytesRefused    int    `json:"pattern_bytes_refused"`
	INC01WorkBudgetRepro   bool   `json:"inc01_work_budget_repro"`
	INC01PartialStdout     bool   `json:"inc01_partial_stdout_before_refusal"`
	INC02PartialStdoutExit bool   `json:"inc02_partial_stdout_exit2"`
	INC02Terminal          string `json:"inc02_terminal"`
	INC02Complete          bool   `json:"inc02_complete"`
	INC02StopCode          string `json:"inc02_stop_code"`
}

func TestCF07G03InProcessBudgetsAndRefusals(t *testing.T) {
	const (
		totalInstances = 200
		seedBase       = "20261127"
	)

	unsupportedPatterns := []string{
		`(?R)`,
		`(?<=a+)b`,
		`(?<!foo+)bar`,
		`(?(1)a|b)`,
		`(?0)`,
		`(?P>sub)`,
	}

	summary := cf07G03Summary{
		Family:         "G03",
		Lane:           "inprocess",
		SeedBase:       seedBase,
		TotalInstances: totalInstances,
	}

	// Shared harness for fast deterministic execution across 200 seeded instances.
	backtrackLine := strings.Repeat("a", 30) + "!\n"
	corpus := map[string]string{
		"input.txt":     "alpha beta\n" + backtrackLine,
		"work.txt":      "match_before_budget\n" + strings.Repeat("a", 28) + "!\n",
		"secondary.txt": "alpha secondary\n",
	}
	h := newHarness(t, corpus)

	for i := 0; i < totalInstances; i++ {
		rng := rand.New(rand.NewSource(cf07G03Seed(seedBase, "G03", i)))
		useRg := rng.Intn(2) == 1
		tool := "grep"
		if useRg {
			tool = "rg"
		}

		if i%2 == 0 {
			// Ceiling 1: GREP_REGEX_UNSUPPORTED
			pat := unsupportedPatterns[rng.Intn(len(unsupportedPatterns))]
			r := h.exec("", tool, "-P", pat, "input.txt")
			r.refused(t, fmt.Sprintf("G03-%03d unsupported %s %q", i, tool, pat), "GREP_REGEX_UNSUPPORTED")
			if r.stdout != "" {
				t.Fatalf("G03-%03d unsupported regex emitted stdout: %q", i, r.stdout)
			}
			summary.RegexUnsupportedPassed++
		} else {
			// Ceiling 2: GREP_MATCH_BUDGET (mix of pattern-bytes ceiling and work-steps ceiling)
			if rng.Intn(4) == 0 {
				// Deterministic regex_work_steps overrun via Perl backtracking on input.txt.
				pat := `(a+)+b`
				r := h.exec("", tool, "-P", pat, "input.txt")
				r.refused(t, fmt.Sprintf("G03-%03d work-steps %s", i, tool), "GREP_MATCH_BUDGET")
				summary.WorkStepsRefused++
			} else {
				// Deterministic regex_pattern_bytes overrun (> 512 bytes).
				extraLen := 520 + rng.Intn(200)
				pat := strings.Repeat("a", extraLen)
				r := h.exec("", tool, pat, "input.txt")
				r.refused(t, fmt.Sprintf("G03-%03d pattern-bytes %s len=%d", i, tool, extraLen), "GREP_MATCH_BUDGET")
				summary.PatternBytesRefused++
			}
			summary.MatchBudgetPassed++
		}
	}

	// Incident edge slot G03-INC-01: partial match emitted before regex_work_steps refusal.
	t.Run("G03-INC-01-work-budget-after-partial-match", func(t *testing.T) {
		incCorpus := map[string]string{
			"01_first.txt":  "aaab\n",
			"02_budget.txt": strings.Repeat("a", 30) + "!\n",
		}
		ih := newHarness(t, incCorpus)
		r := ih.exec("", "rg", "-P", `(a+)+b`, "01_first.txt", "02_budget.txt")
		r.refused(t, "G03-INC-01 work budget", "GREP_MATCH_BUDGET")
		summary.INC01WorkBudgetRepro = true
		summary.INC01PartialStdout = strings.Contains(r.stdout, "aaab")
	})

	// Incident edge slot G03-INC-02: partial stdout + missing/unreadable target exits 2.
	t.Run("G03-INC-02-partial-stdout-exit-2", func(t *testing.T) {
		incCorpus := map[string]string{
			"good.txt": "alpha hit\n",
		}
		ih := newHarness(t, incCorpus)
		r := ih.exec("", "grep", "alpha", "good.txt", "unreadable_or_missing.txt")
		if r.result.Status != 2 || !strings.Contains(r.stdout, "good.txt:alpha hit") || r.stderr == "" {
			t.Fatalf("G03-INC-02 unexpected result: status=%d stdout=%q stderr=%q terminal=%v",
				r.result.Status, r.stdout, r.stderr, r.result.Terminal)
		}
		if r.result.StopCode != "" {
			t.Fatalf("G03-INC-02 expected empty stop_code, got %q", r.result.StopCode)
		}
		summary.INC02PartialStdoutExit = true
		summary.INC02Terminal = string(r.result.Terminal)
		summary.INC02Complete = r.result.Complete
		summary.INC02StopCode = r.result.StopCode
	})

	encoded, err := json.Marshal(summary)
	testutil.FailErr(t, "marshal g03 summary", err)
	t.Logf("CF07_G03_INPROCESS_SUMMARY: %s", string(encoded))
}
