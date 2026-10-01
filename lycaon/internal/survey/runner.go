package survey

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/sandbox"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
)

// ErrSurveyPathEscape reports a structured survey scope violation.
var ErrSurveyPathEscape = errors.New("survey path escape")

// RunResult is the deterministic output of a survey bundle run.
type RunResult struct {
	BundleID       string
	Path           string
	Ledger         evidence.Ledger
	Digest         string
	ProbesRun      int
	ProbeSummaries []ProbeSummary
	Coverage       ProbeCoverage
}

// Runner executes catalog survey bundles.
type Runner struct {
	Catalog *Catalog
	Caps    Caps
}

// NewRunner returns a runner with catalog and caps.
func NewRunner(catalog *Catalog, caps Caps) *Runner {
	return &Runner{Catalog: catalog, Caps: caps.withDefaults()}
}

// Run executes bundleID under relPath within scope.
func (r *Runner) Run(ctx context.Context, bundleID, relPath string, scope Scope) (*RunResult, error) {
	if r == nil || r.Catalog == nil {
		return nil, fmt.Errorf("survey runner: nil catalog")
	}
	bundle, ok := r.Catalog.Bundles[bundleID]
	if !ok {
		return nil, fmt.Errorf("survey bundle %q not found", bundleID)
	}
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		relPath = "."
	}
	if sandbox.HasParentTraversal(relPath) {
		return nil, fmt.Errorf("%w: %q", ErrSurveyPathEscape, relPath)
	}

	probes := bundle.Probes
	type probeResult struct {
		Probe     Probe
		Records   []evidence.Record
		Total     int
		Coverage  ProbeCoverage
		GrepStats surveytools.SurveyGrepBatchStats
		Err       error
	}
	results := make([]probeResult, len(probes))
	for i := 0; i < len(probes); {
		if results[i].Probe.Kind != "" {
			i++
			continue
		}
		if err := ctx.Err(); err != nil {
			results[i] = probeResult{Probe: probes[i], Err: err}
			i++
			continue
		}
		probe := probes[i]
		if probe.Kind != ProbeGrep {
			recs, total, coverage, err := r.runProbe(ctx, probe, relPath, scope)
			results[i] = probeResult{Probe: probe, Records: recs, Total: total, Coverage: coverage, Err: err}
			i++
			continue
		}
		path := effectiveProbePath(probe, relPath)
		batchCap := r.Caps.MaxProbeBatch
		if batchCap <= 0 {
			batchCap = len(probes)
		}
		indices := make([]int, 0, min(batchCap, len(probes)-i))
		specs := make([]surveytools.SurveyGrepSpec, 0, min(batchCap, len(probes)-i))
		for j := i; j < len(probes); j++ {
			if results[j].Probe.Kind == "" && probes[j].Kind == ProbeGrep && effectiveProbePath(probes[j], relPath) == path {
				indices = append(indices, j)
				specs = append(specs, surveytools.SurveyGrepSpec{Label: probes[j].Label, Pattern: probes[j].Pattern})
				if len(indices) == batchCap {
					break
				}
			}
		}
		batch, batchStats, batchErr := surveytools.ProbeGrepBatchRecords(ctx, scope.Boundary, scope.SourceCatalog,
			scope.ToolCtx, path, specs, r.Caps.GrepMatchCap)
		for k, index := range indices {
			var recs []evidence.Record
			var total int
			err := batchErr
			var stats surveytools.SurveyGrepBatchStats
			if batchErr == nil {
				recs = batch[k].Records
				total = batch[k].MatchCount
				stats = batchStats
			} else {
				recs, total, stats, err = surveytools.ProbeGrepRecords(ctx, scope.Boundary, scope.SourceCatalog, scope.ToolCtx, path,
					probes[index].Pattern, probes[index].Label, r.Caps.GrepMatchCap)
			}
			results[index] = probeResult{Probe: probes[index], Records: recs, Total: total, GrepStats: stats, Err: err}
		}
		i++
		for i < len(probes) && results[i].Probe.Kind != "" {
			i++
		}
	}

	var tagged []taggedRecord
	summaries := make([]ProbeSummary, 0, len(bundle.Probes))
	probesRun := 0
	coverage := ProbeCoverage{}
	for _, pr := range results {
		sum := ProbeSummary{
			Label:    pr.Probe.Label,
			Kind:     pr.Probe.Kind,
			Priority: pr.Probe.Priority,
		}
		if pr.Err != nil {
			sum.Err = pr.Err.Error()
			summaries = append(summaries, sum)
			continue
		}
		probesRun++
		raw := len(pr.Records)
		if pr.Total > 0 {
			raw = pr.Total
		}
		sum.MatchCount = raw
		sum.SampleCount = len(pr.Records)
		sum.Sampled = (pr.Probe.Kind == ProbeGrep || pr.Probe.Kind == ProbeFind) && raw > len(pr.Records)
		sum.FilesExamined = pr.GrepStats.FilesOpened
		sum.FilesBytesTruncated = pr.GrepStats.FilesBytesTruncated
		sum.BinarySkipped = pr.GrepStats.BinarySkipped
		sum.FilesUnreadable = pr.GrepStats.FilesUnreadable
		emitted, nEmitted, folded := emitWithRollup(pr.Probe.Label, pr.Probe.Priority, pr.Records, pr.Probe.EmitCap, sum.Sampled)
		sum.Emitted = nEmitted
		sum.Folded = folded
		sum.Rolled = pr.Probe.EmitCap > 0 && raw > pr.Probe.EmitCap
		summaries = append(summaries, sum)
		tagged = append(tagged, emitted...)
		if pr.Coverage.Resolution != "" {
			coverage = pr.Coverage
		}
	}
	tagged = retainCandidates(tagged, r.Caps.MaxRetainedRecords)
	ledger := ledgerFromTagged(tagged)

	return &RunResult{
		BundleID:       bundleID,
		Path:           relPath,
		Ledger:         ledger,
		Digest:         buildDigest(bundleID, relPath, summaries, ledger, coverage),
		ProbesRun:      probesRun,
		ProbeSummaries: summaries,
		Coverage:       coverage,
	}, nil
}

func effectiveProbePath(probe Probe, relPath string) string {
	path := strings.TrimSpace(probe.Path)
	if path == "" || path == "." && relPath != "." {
		return relPath
	}
	return path
}

func (r *Runner) runProbe(ctx context.Context, probe Probe, relPath string, scope Scope) ([]evidence.Record, int, ProbeCoverage, error) {
	path := effectiveProbePath(probe, relPath)
	switch probe.Kind {
	case ProbeFind:
		recs, total, err := surveytools.ProbeFindRecords(ctx, scope.Boundary, scope.SourceCatalog, scope.ToolCtx, path, probe.NameGlob, probe.Label, r.Caps.FindMatchCap)
		return recs, total, ProbeCoverage{}, err
	case ProbeListDir:
		return runLayoutProbe(ctx, probe, path, scope, r.Caps.LayoutGroupCap)
	default:
		return nil, 0, ProbeCoverage{}, fmt.Errorf("unsupported probe kind %q", probe.Kind)
	}
}
