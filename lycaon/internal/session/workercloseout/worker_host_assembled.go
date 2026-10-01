package workercloseout

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
)

// bindWorkerObservedSample adds a bounded sample of observed child evidence.
func bindWorkerObservedSample(ctx context.Context, report workercompletion.WorkerCompletionReport, opts WorkerSummaryFinalizeOpts, childSessionID string) (workercompletion.WorkerCompletionReport, bool, error) {
	if opts.Ledger == nil {
		return report, false, nil
	}
	ev, err := opts.Ledger.LoadLedger(ctx, childSessionID)
	if err != nil {
		return report, false, fmt.Errorf("load worker evidence for host assembly: %w", err)
	}

	report.Findings = groundedWorkerFindings(report.Findings, opts.ProjectDir, ev)
	observedURLs := make(map[string]struct{})
	for _, u := range evidence.ObservedURLsSorted(ev) {
		observedURLs[strings.TrimSpace(u)] = struct{}{}
	}
	existingURLs := make(map[string]struct{})
	filteredURLs := make([]string, 0, len(report.CitedURLs))
	for _, u := range report.CitedURLs {
		u = strings.TrimSpace(u)
		if _, ok := observedURLs[u]; !ok {
			continue
		}
		if _, duplicate := existingURLs[u]; duplicate {
			continue
		}
		existingURLs[u] = struct{}{}
		filteredURLs = append(filteredURLs, u)
	}
	report.CitedURLs = filteredURLs
	report.CitedURLs = append(report.CitedURLs, guidance.SelectObservedURLSample(ev, report.Brief, existingURLs)...)

	existingPaths := map[string]struct{}{}
	for _, f := range report.Findings {
		existingPaths[f.Path] = struct{}{}
	}
	for _, sample := range guidance.SelectObservedPathSample(ev, evidence.CitationRoots{ProjectDir: opts.ProjectDir}, report.Brief, existingPaths) {
		report.Findings = append(report.Findings, workercompletion.WorkerFinding{
			Path: sample.Path,
			Line: sample.Line,
		})
	}
	report.Normalize()
	return report, true, nil
}

func groundedWorkerFindings(findings []workercompletion.WorkerFinding, projectDir string, ev evidence.Ledger) []workercompletion.WorkerFinding {
	out := make([]workercompletion.WorkerFinding, 0, len(findings))
	for _, finding := range findings {
		resolved := evidence.Resolve(evidence.CitationRoots{ProjectDir: projectDir}, evidence.Triple{
			Path: finding.Path, Line: finding.Line, Excerpt: finding.Excerpt,
		}, ev, "")
		if !resolved.Verdict.Grounded() {
			continue
		}
		finding.Path = resolved.Path
		finding.Line = resolved.Line
		finding.Excerpt = resolved.Excerpt
		out = append(out, finding)
	}
	return out
}
