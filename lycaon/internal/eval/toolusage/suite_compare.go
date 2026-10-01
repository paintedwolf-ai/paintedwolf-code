package toolusage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
)

func LoadSuiteReport(path string) (SuiteReport, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- operator-selected evaluation report.
	if err != nil {
		return SuiteReport{}, err
	}
	var report SuiteReport
	if err := json.Unmarshal(body, &report); err != nil {
		return report, err
	}
	if report.Version != 1 || report.SuiteID == "" {
		return report, fmt.Errorf("not a version 1 suite report")
	}
	for _, c := range report.Cases {
		if c.Review == nil {
			continue
		}
		if c.Review.Reviewer == "" || c.Review.Notes == "" {
			return report, fmt.Errorf("case %s review requires reviewer and notes", c.ID)
		}
		switch c.Review.Outcome {
		case "passed", "partial", "blocked", "failed":
		default:
			return report, fmt.Errorf("case %s has invalid review outcome", c.ID)
		}
	}
	return report, nil
}

// CompareSuites reports outcomes before costs and refuses mismatched test inputs.
func CompareSuites(w io.Writer, baseline, candidate SuiteReport) error {
	if len(baseline.Cases) == 0 || len(candidate.Cases) == 0 {
		return fmt.Errorf("comparison requires results in both reports")
	}
	if baseline.SuiteSHA256 == "" || baseline.SuiteSHA256 != candidate.SuiteSHA256 {
		return fmt.Errorf("suite definitions differ; results are not a paired comparison")
	}
	inputs := map[string]string{}
	baselineCounts := map[string]int{}
	for _, c := range baseline.Cases {
		inputs[c.ID] = c.FixtureSHA256
		baselineCounts[c.ID]++
	}
	candidateCounts := map[string]int{}
	for _, c := range candidate.Cases {
		if inputs[c.ID] == "" || inputs[c.ID] != c.FixtureSHA256 {
			return fmt.Errorf("fixture or case differs for %s", c.ID)
		}
		candidateCounts[c.ID]++
	}
	if !maps.Equal(baselineCounts, candidateCounts) {
		return fmt.Errorf("case sets or repetition counts differ; select equal-sized samples before comparing totals")
	}
	for _, report := range []SuiteReport{baseline, candidate} {
		counts := map[string]int{}
		var elapsed int64
		var missingUsage int
		for _, c := range report.Cases {
			outcome := c.Status
			if c.Review != nil && c.Status == "review_required" {
				outcome = c.Review.Outcome
			}
			counts[outcome]++
			elapsed += c.DurationMS
			if c.Profile != nil {
				missingUsage += c.Profile.TokenSpend.MissingUsageCalls
			}
		}
		fmt.Fprintf(w, "%s (%s): cases=%d passed=%d partial=%d blocked=%d failed=%d errors=%d unreviewed=%d; prompt_tokens=%d wall=%.1fs\n",
			report.Label, report.ExpectedModel, len(report.Cases), counts["passed"], counts["partial"], counts["blocked"], counts["failed"], counts["error"], counts["review_required"], report.PromptTokens, float64(elapsed)/1000)
		if missingUsage > 0 {
			fmt.Fprintf(w, "  Usage missing for %d captured calls; token totals are a lower bound, not a complete cost comparison.\n", missingUsage)
		}
	}
	fmt.Fprintln(w, "Compare outcomes before token cost; unreviewed is not passed. Small samples are directional, not reliability estimates.")
	return nil
}

// RefreshSuiteReport replays settled captures without running an agent or changing outcome reviews.
// A live stream can publish its answer before its final capture row reaches disk.
func RefreshSuiteReport(path string) error {
	report, err := LoadSuiteReport(path)
	if err != nil {
		return err
	}
	report.PromptTokens = 0
	for i := range report.Cases {
		c := &report.Cases[i]
		if c.SessionID == "" {
			continue
		}

		if err := settleCapturedExecution(context.Background(), report.CaptureDir, c); err != nil {
			return fmt.Errorf("settle case %s: %w", c.ID, err)
		}

		profile, err := ProfileFromCaptureSession(report.CaptureDir, c.SessionID)
		if err == nil && profile.Model != report.ExpectedModel {
			err = fmt.Errorf("model %q differs from expected %q", profile.Model, report.ExpectedModel)
		}
		if err != nil {
			if c.Status != "blocked" && c.Status != "error" {
				return fmt.Errorf("refresh case %s: %w", c.ID, err)
			}
			c.CaptureError = err.Error()
			if c.Profile != nil {
				report.PromptTokens += c.Profile.TokenSpend.PromptTokens
			}
			continue
		}
		c.CaptureError = ""
		c.Profile = &profile
		report.PromptTokens += profile.TokenSpend.PromptTokens
	}
	return saveSuiteReport(path, report)
}
