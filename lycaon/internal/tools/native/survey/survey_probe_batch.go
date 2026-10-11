package survey

import (
	"context"
	"hash/fnv"
	"os"
	"sort"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// SurveyGrepSpec is one pattern in a composite survey scan.
type SurveyGrepSpec struct {
	Label   string
	Pattern string
}

// SurveyGrepBatchResult is a full-scope count plus a bounded representative sample.
type SurveyGrepBatchResult struct {
	Records    []evidence.Record
	MatchCount int
}

// SurveyGrepBatchStats reports shared work performed by a composite grep pass.
type SurveyGrepBatchStats struct {
	FilesOpened         int
	FilesBytesTruncated int
	BinarySkipped       int
	FilesUnreadable     int
}

type surveyGrepAccumulator struct {
	capacity int
	seed     uint64
	total    int
	sample   []grepMatch
}

// ProbeGrepBatchRecords scans each file once and retains bounded samples.
func ProbeGrepBatchRecords(
	ctx context.Context,
	boundary *sandbox.Boundary,
	catalog *sourcecatalog.Catalog,
	tctx tools.ToolContext,
	relPath string,
	specs []SurveyGrepSpec,
	maxMatches int,
) ([]SurveyGrepBatchResult, SurveyGrepBatchStats, error) {
	stats := SurveyGrepBatchStats{}
	reads := projectpaths.NewReadSession(boundary, tctx)
	defer reads.Close()
	searches := make([]*grepSearch, len(specs))
	accumulators := make([]surveyGrepAccumulator, len(specs))
	for i, spec := range specs {
		opts, err := parseGrepArgs(map[string]any{
			"pattern": spec.Pattern, "path": relPath, "max_matches": maxMatches,
		})
		if err != nil {
			return nil, stats, err
		}
		re, err := compileGrepPattern(opts)
		if err != nil {
			return nil, stats, err
		}
		searches[i] = &grepSearch{
			ctx: ctx, boundary: boundary, tctx: tctx, reads: reads, pattern: opts.pattern, re: re,
			structural: opts.structural, lang: opts.lang, maxMatches: maxMatches,
			contextLines: 0, require: litprefilter.Extract(opts.pattern, opts.caseInsensitive),
			resp: &grepResponse{Matches: []grepMatch{}},
		}
		accumulators[i] = surveyGrepAccumulator{
			capacity: maxMatches, seed: surveySampleSeed(spec.Label, spec.Pattern),
			sample: make([]grepMatch, 0, maxMatches),
		}
		searches[i].matchSink = accumulators[i].add
	}
	tool := &GrepTool{Boundary: boundary, Catalog: catalog}
	_, targets, err := tool.resolveGrepTargets(ctx, tctx, map[string]any{"path": relPath})
	if err != nil {
		return nil, stats, err
	}
	for _, target := range targets {
		if err := scanSurveyProbeTarget(ctx, tool, reads, tctx, target, searches, &stats); err != nil {
			return nil, stats, err
		}
	}
	out := make([]SurveyGrepBatchResult, len(specs))
	for i, spec := range specs {
		accumulators[i].sort()
		out[i] = SurveyGrepBatchResult{
			Records: grepProbeRecords(spec.Label, accumulators[i].sample), MatchCount: accumulators[i].total,
		}
	}
	return out, stats, nil
}

func scanSurveyProbeTarget(
	ctx context.Context,
	tool *GrepTool,
	reads *projectpaths.ReadSession,
	tctx tools.ToolContext,
	target grepTarget,
	searches []*grepSearch,
	stats *SurveyGrepBatchStats,
) error {
	info, err := os.Stat(target.fullRoot)
	if err != nil {
		return grepRootStatErr(target.displayRoot, err)
	}
	if !info.IsDir() {
		return scanSurveyProbeFile(ctx, reads, target.fullRoot, target.displayRoot, info, searches, stats)
	}
	if tctx.Source.WorkerBranchRoot != "" {
		return scanSurveyProbeTree(ctx, tool, reads, tctx, target, searches, stats)
	}
	var scanErr error
	err = tool.forEachGrepCatalogJob(ctx, tctx, target, false, sandbox.SurveyOptions{}, nil, func(job grepFileJob) bool {
		info, statErr := os.Lstat(job.abs)
		if statErr != nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return true
		}
		scanErr = scanSurveyProbeFile(ctx, reads, job.abs, job.rel, info, searches, stats)
		return scanErr == nil
	})
	if err != nil {
		return err
	}
	return scanErr
}

func scanSurveyProbeTree(
	ctx context.Context,
	tool *GrepTool,
	reads *projectpaths.ReadSession,
	tctx tools.ToolContext,
	target grepTarget,
	searches []*grepSearch,
	stats *SurveyGrepBatchStats,
) error {
	walkOpts, err := tool.grepSurveyOptions(ctx, tctx, target, false, sandbox.SurveyOptions{})
	if err != nil {
		return err
	}
	return workerBranchOrSurveyWalk(ctx, tctx, target.fullRoot, walkOpts, func(entry sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
		if entry.IsDir {
			return sandbox.SurveyContinue, nil
		}
		if ensureErr := ensureBranchFileForRead(ctx, tctx, entry.Abs); ensureErr != nil {
			return sandbox.SurveyContinue, nil
		}
		info, infoErr := entry.DirEntry.Info()
		if infoErr != nil {
			return sandbox.SurveyContinue, nil
		}
		rel := projectpaths.QualifyAbs(tctx, target.root, entry.Abs)
		if scanErr := scanSurveyProbeFile(ctx, reads, entry.Abs, rel, info, searches, stats); scanErr != nil {
			return sandbox.SurveyStop, scanErr
		}
		return sandbox.SurveyContinue, nil
	})
}

func scanSurveyProbeFile(
	ctx context.Context,
	reads *projectpaths.ReadSession,
	abs, rel string,
	info os.FileInfo,
	searches []*grepSearch,
	stats *SurveyGrepBatchStats,
) error {
	content, binary, bytesTruncated, err := readGrepFile(ctx, reads, abs, info)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if stats != nil {
			stats.FilesUnreadable++
		}
		return nil
	}
	if stats != nil {
		stats.FilesOpened++
		if bytesTruncated {
			stats.FilesBytesTruncated++
		}
	}
	if binary {
		if stats != nil {
			stats.BinarySkipped++
		}
		return nil
	}
	for _, search := range searches {
		if err := search.scanFile(ctx, rel, content, bytesTruncated); err != nil {
			return err
		}
	}
	return nil
}

func (a *surveyGrepAccumulator) add(match grepMatch) {
	a.total++
	if a.capacity <= 0 {
		return
	}
	if len(a.sample) < a.capacity {
		a.sample = append(a.sample, match)
		return
	}
	pick := splitMix64(a.seed+uint64(a.total)) % uint64(a.total)
	if pick < uint64(a.capacity) {
		a.sample[pick] = match
	}
}

func (a *surveyGrepAccumulator) sort() {
	sort.Slice(a.sample, func(i, j int) bool {
		if a.sample[i].Path != a.sample[j].Path {
			return a.sample[i].Path < a.sample[j].Path
		}
		if a.sample[i].Line != a.sample[j].Line {
			return a.sample[i].Line < a.sample[j].Line
		}
		return a.sample[i].Content < a.sample[j].Content
	})
}

func surveySampleSeed(label, pattern string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(label))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(pattern))
	return h.Sum64()
}

func splitMix64(value uint64) uint64 {
	value += 0x9e3779b97f4a7c15
	value = (value ^ value>>30) * 0xbf58476d1ce4e5b9
	value = (value ^ value>>27) * 0x94d049bb133111eb
	return value ^ value>>31
}
