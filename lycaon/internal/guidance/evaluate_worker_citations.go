package guidance

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// Worker citation grounding codes (guard:worker_summary).
const (
	WorkerEvidenceHandleUnknownCode = "WORKER_EVIDENCE_HANDLE_UNKNOWN"
	WorkerExcerptHandleMismatchCode = "WORKER_EXCERPT_HANDLE_MISMATCH"
	WorkerURLNotObservedCode        = "WORKER_URL_NOT_OBSERVED"
	SurfaceClaimUngroundedCode      = "SURFACE_CLAIM_UNGROUNDED"
	PageMeasureUngroundedCode       = "PAGE_MEASURE_UNGROUNDED"
)

// WorkerFindingInput is one typed finding citation from a completion report.
type WorkerFindingInput struct {
	Path     string
	Evidence string // optional handle hint for evidence.Resolve
	Line     int
	Excerpt  string
}

// WorkerCitationEval is the outcome of typed worker citation grounding.
type WorkerCitationEval struct {
	Code                 string
	Offenders            []string
	UnobservedURLs       []string
	Resolutions          []evidence.Resolution
	ProseDuplicateTokens []string
	ProseDuplicateCount  int
	ProseAdvisoryTokens  []string
	ProseAdvisoryCount   int
	BindAdvisoryTokens   []string
	BindAdvisoryCount    int
	SurveyAdvisoryTokens []string
	SurveyAdvisoryCount  int
}

// EvaluateWorkerCitations checks typed report fields against captured evidence.
func EvaluateWorkerCitations(
	roots evidence.CitationRoots,
	findings []WorkerFindingInput,
	citedURLs []string,
	narrative WorkerNarrativeInput,
	ev evidence.Ledger,
) WorkerCitationEval {
	unobservedURLs := citedURLHandleOffenders(citedURLs, ev)
	resolved, unverifiable, survey, surfaceUngrounded, pageMeasure := resolveWorkerFindingCitations(roots, findings, ev)
	if len(surfaceUngrounded) > 0 {
		return WorkerCitationEval{
			Code:                 SurfaceClaimUngroundedCode,
			Offenders:            surfaceUngrounded,
			UnobservedURLs:       unobservedURLs,
			Resolutions:          resolved,
			SurveyAdvisoryTokens: survey,
			SurveyAdvisoryCount:  len(survey),
		}
	}
	if len(pageMeasure) > 0 {
		return WorkerCitationEval{
			Code:                 PageMeasureUngroundedCode,
			Offenders:            pageMeasure,
			UnobservedURLs:       unobservedURLs,
			Resolutions:          resolved,
			SurveyAdvisoryTokens: survey,
			SurveyAdvisoryCount:  len(survey),
		}
	}
	if len(unverifiable) > 0 {
		return WorkerCitationEval{
			Code:                 WorkerEvidenceHandleUnknownCode,
			Offenders:            unverifiable,
			UnobservedURLs:       unobservedURLs,
			Resolutions:          resolved,
			SurveyAdvisoryTokens: survey,
			SurveyAdvisoryCount:  len(survey),
		}
	}
	if len(unobservedURLs) > 0 {
		return WorkerCitationEval{
			Code:           WorkerURLNotObservedCode,
			Offenders:      unobservedURLs,
			UnobservedURLs: unobservedURLs,
			Resolutions:    resolved,
		}
	}
	typed := BuildWorkerTypedChannel(roots, ev, findings, citedURLs)
	proseEval := EvaluateWorkerProseLeaks(narrative, ev, typed)
	bound := BindAdvisoryTokens(resolved)
	return WorkerCitationEval{
		Resolutions:          resolved,
		UnobservedURLs:       unobservedURLs,
		ProseDuplicateTokens: proseEval.DuplicateTokens,
		ProseDuplicateCount:  len(proseEval.DuplicateTokens),
		ProseAdvisoryTokens:  proseEval.AdvisoryLeakTokens,
		ProseAdvisoryCount:   len(proseEval.AdvisoryLeakTokens),
		BindAdvisoryTokens:   bound,
		BindAdvisoryCount:    len(bound),
		SurveyAdvisoryTokens: survey,
		SurveyAdvisoryCount:  len(survey),
	}
}

// LedgerHasSurveyHandle reports whether the ledger contains a successful survey-kind handle.
func LedgerHasSurveyHandle(ev evidence.Ledger) bool {
	binding := evidence.ActiveBinding()
	for _, rec := range ev.Handles {
		if binding != nil && binding.IsSurveyKind(rec.Kind) {
			return true
		}
	}
	return false
}

// EvaluateSurfaceClaimComplete requires a capture for visual work.
func EvaluateSurfaceClaimComplete(legStatus string, ev evidence.Ledger) (code string, offenders []string) {
	if !strings.EqualFold(strings.TrimSpace(legStatus), "complete") {
		return "", nil
	}
	if !evidence.LedgerHasVisualIntent(ev) {
		return "", nil
	}
	if evidence.LedgerHasSurfaceSnapshot(ev) {
		return "", nil
	}
	offenders = make([]string, 0)
	for handle, rec := range ev.Handles {
		if evidence.RecordShape(rec) == evidence.ShapeVisual {
			offenders = append(offenders, handle)
		}
	}
	sort.Strings(offenders)
	return SurfaceClaimUngroundedCode, offenders
}

// PrependHandleTag prefixes captured tool output with its evidence handle tag.
func PrependHandleTag(content, handle string) string {
	handle = strings.TrimSpace(handle)
	content = strings.TrimSpace(content)
	if handle == "" {
		return content
	}
	tag := "[" + handle + "]"
	if content == "" {
		return tag
	}
	return tag + "\n" + content
}

func normalizedReportPath(roots evidence.CitationRoots, raw string, byPath map[string][]string) string {
	if key := evidence.LedgerPathKey(roots, raw, byPath); key != "" {
		return key
	}
	slash := filepath.ToSlash(strings.TrimSpace(raw))
	if slash != "" && !sandbox.HasParentTraversal(slash) {
		return slash
	}
	return ""
}

// resolveWorkerFindingCitations returns one resolution per finding, at the
// finding's index, so callers can write corrections back by position.
func resolveWorkerFindingCitations(
	roots evidence.CitationRoots,
	findings []WorkerFindingInput,
	ev evidence.Ledger,
) (resolved []evidence.Resolution, unverifiable, surveyAdvisories, surfaceUngrounded, pageMeasure []string) {
	resolved = make([]evidence.Resolution, len(findings))
	seen := map[string]struct{}{}
	surveySeen := map[string]struct{}{}
	surfaceSeen := map[string]struct{}{}
	measureSeen := map[string]struct{}{}
	for i, finding := range findings {
		path := strings.TrimSpace(finding.Path)
		handle := strings.TrimSpace(finding.Evidence)
		if path == "" && handle == "" {
			resolved[i] = evidence.Resolution{Verdict: evidence.VerdictUnverifiable}
			continue
		}
		line := finding.Line
		excerpt := strings.TrimSpace(finding.Excerpt)
		if path != "" && line <= 0 && excerpt == "" {
			// Survey-grade citations are advisory.
			if citationResolvesToSurveyRecord(roots, finding, ev) {
				addOffender(&surveyAdvisories, surveySeen, path)
				resolved[i] = evidence.Resolution{Path: path, Verdict: evidence.VerdictTraced}
				continue
			}
			addOffender(&unverifiable, seen, path)
			resolved[i] = evidence.Resolution{Path: path, Verdict: evidence.VerdictUnverifiable}
			continue
		}
		// Bare handles still require ledger membership.
		if line <= 0 && excerpt == "" {
			res, found := resolveBareHandleCitation(ev, handle)
			resolved[i] = res
			if !found {
				addOffender(&unverifiable, seen, handle)
			} else if citationResolvesToSurveyRecord(roots, finding, ev) {
				addOffender(&surveyAdvisories, surveySeen, handle)
			}
			continue
		}
		// Authored visual intent cannot ground runtime observations.
		if citationResolvesToVisualIntent(finding, ev) {
			token := handle
			if token == "" {
				token = path
			}
			addOffender(&surfaceUngrounded, surfaceSeen, token)
			resolved[i] = evidence.Resolution{Handle: handle, Path: path, Line: line, Excerpt: excerpt, Verdict: evidence.VerdictUnverifiable}
			continue
		}
		res := resolveWorkerFindingCitation(roots, finding, ev)
		resolved[i] = res
		if res.Verdict.Grounded() {
			// Page measurements require captured text.
			if res.Verdict == evidence.VerdictTraced && isPageMeasureUngrounded(finding, ev, excerpt) {
				token := handle
				if token == "" {
					token = FormatResolutionOffender(res, finding)
				}
				addOffender(&pageMeasure, measureSeen, token)
			}
			continue
		}
		// Survey-grade citations are advisory.
		if citationResolvesToSurveyRecord(roots, finding, ev) {
			addOffender(&surveyAdvisories, surveySeen, FormatResolutionOffender(res, finding))
			continue
		}
		// Surface measurements require geometry evidence.
		if isPageMeasureUngrounded(finding, ev, excerpt) {
			token := handle
			if token == "" {
				token = FormatResolutionOffender(res, finding)
			}
			addOffender(&pageMeasure, measureSeen, token)
			continue
		}
		addOffender(&unverifiable, seen, FormatResolutionOffender(res, finding))
	}
	return resolved, unverifiable, surveyAdvisories, surfaceUngrounded, pageMeasure
}

// isPageMeasureUngrounded reports a layout number cited against a page capture
// that the capture does not contain. Unrelated geometry records elsewhere in
// the ledger do not ground the claim.
func isPageMeasureUngrounded(finding WorkerFindingInput, ev evidence.Ledger, excerpt string) bool {
	excerpt = strings.TrimSpace(excerpt)
	if excerpt == "" {
		return false
	}
	rec, ok := resolveCitedRecord(finding, ev)
	if !ok || evidence.RecordShape(rec) != evidence.ShapeSurfaceSnapshot {
		return false
	}
	if ok, _ := evidence.VerifyRecord(rec, evidence.Claim{Excerpt: excerpt}); ok {
		return false
	}
	return true
}

func resolveCitedRecord(finding WorkerFindingInput, ev evidence.Ledger) (evidence.Record, bool) {
	if handle := strings.TrimSpace(finding.Evidence); handle != "" {
		return evidence.ResolveHandle(ev, handle)
	}
	return evidence.Record{}, false
}

// resolveCloseoutFindingCitations separates missing and unverifiable citations.
func resolveCloseoutFindingCitations(
	roots evidence.CitationRoots,
	findings []WorkerFindingInput,
	ev evidence.Ledger,
) (resolved []evidence.Resolution, notObserved, unverifiable, surveyAdvisories []string) {
	seenNotObserved := map[string]struct{}{}
	seenUnverifiable := map[string]struct{}{}
	surveySeen := map[string]struct{}{}
	for _, finding := range findings {
		path := strings.TrimSpace(finding.Path)
		handle := strings.TrimSpace(finding.Evidence)
		if path == "" && handle == "" {
			continue
		}
		line := finding.Line
		excerpt := strings.TrimSpace(finding.Excerpt)
		if path != "" && line <= 0 && excerpt == "" {
			if citationResolvesToSurveyRecord(roots, finding, ev) {
				addOffender(&surveyAdvisories, surveySeen, path)
				continue
			}
			if citationWasObserved(roots, finding, ev) {
				addOffender(&unverifiable, seenUnverifiable, path)
			} else {
				addOffender(&notObserved, seenNotObserved, path)
			}
			continue
		}
		// Bare handles still require ledger membership.
		if line <= 0 && excerpt == "" {
			res, found := resolveBareHandleCitation(ev, handle)
			resolved = append(resolved, res)
			if !found {
				addOffender(&notObserved, seenNotObserved, handle)
			} else if citationResolvesToSurveyRecord(roots, finding, ev) {
				addOffender(&surveyAdvisories, surveySeen, handle)
			}
			continue
		}
		if citationResolvesToVisualIntent(finding, ev) {
			token := handle
			if token == "" {
				token = path
			}
			addOffender(&unverifiable, seenUnverifiable, token)
			continue
		}
		res := resolveWorkerFindingCitation(roots, finding, ev)
		resolved = append(resolved, res)
		if res.Verdict.Grounded() {
			continue
		}
		if citationResolvesToSurveyRecord(roots, finding, ev) {
			addOffender(&surveyAdvisories, surveySeen, FormatResolutionOffender(res, finding))
			continue
		}
		offender := FormatResolutionOffender(res, finding)
		if citationWasObserved(roots, finding, ev) {
			addOffender(&unverifiable, seenUnverifiable, offender)
		} else {
			addOffender(&notObserved, seenNotObserved, offender)
		}
	}
	return resolved, notObserved, unverifiable, surveyAdvisories
}

// citationWasObserved checks handle and path membership in the ledger.
func citationWasObserved(roots evidence.CitationRoots, finding WorkerFindingInput, ev evidence.Ledger) bool {
	if handle := strings.TrimSpace(finding.Evidence); handle != "" {
		if _, ok := evidence.ResolveHandle(ev, handle); ok {
			return true
		}
	}
	path := strings.TrimSpace(finding.Path)
	if path == "" {
		return false
	}
	if p, _, ok := evidence.SplitPathLineToken(path); ok {
		path = p
	}
	norm := normalizedReportPath(roots, path, ev.ByPath)
	if norm == "" {
		return false
	}
	return len(ev.ByPath[norm]) > 0
}

// citationResolvesToSurveyRecord checks the captured survey flag.
func citationResolvesToSurveyRecord(roots evidence.CitationRoots, finding WorkerFindingInput, ev evidence.Ledger) bool {
	if handle := strings.TrimSpace(finding.Evidence); handle != "" {
		if rec, ok := evidence.ResolveHandle(ev, handle); ok && rec.Survey {
			return true
		}
	}
	path := strings.TrimSpace(finding.Path)
	if path == "" {
		return false
	}
	if p, _, ok := evidence.SplitPathLineToken(path); ok {
		path = p
	}
	norm := normalizedReportPath(roots, path, ev.ByPath)
	if norm == "" {
		return false
	}
	for _, handle := range ev.ByPath[norm] {
		if rec, ok := evidence.ResolveHandle(ev, handle); ok && rec.Survey {
			return true
		}
	}
	return false
}

// citationResolvesToVisualIntent checks the captured shape.
func citationResolvesToVisualIntent(finding WorkerFindingInput, ev evidence.Ledger) bool {
	return evidence.CitationResolvesToVisualIntent(finding.Evidence, ev)
}

// resolveWorkerFindingCitation permits a unique ledger match after exact resolution.
func resolveWorkerFindingCitation(
	roots evidence.CitationRoots,
	finding WorkerFindingInput,
	ev evidence.Ledger,
) evidence.Resolution {
	res := resolveWorkerFindingCitationExact(roots, finding, ev)
	if _, known := evidence.ResolveHandle(ev, finding.Evidence); !known && res.Verdict == evidence.VerdictUnverifiable {
		res = bindLooseCitation(roots.ProjectDir, finding, ev, res)
	}
	return res
}

func resolveWorkerFindingCitationExact(
	roots evidence.CitationRoots,
	finding WorkerFindingInput,
	ev evidence.Ledger,
) evidence.Resolution {
	path := strings.TrimSpace(finding.Path)
	handle := strings.TrimSpace(finding.Evidence)
	line := finding.Line
	excerpt := strings.TrimSpace(finding.Excerpt)

	if _, known := evidence.ResolveHandle(ev, handle); known {
		return evidence.ResolveObservation(roots, ev, handle, evidence.Triple{Path: path, Line: line, Excerpt: excerpt})
	}
	if path != "" {
		res := evidence.Resolve(roots, evidence.Triple{
			Path: path, Line: line, Excerpt: excerpt,
		}, ev, handle)
		if strings.TrimSpace(res.Path) == "" {
			res.Path = evidence.LedgerPathKey(roots, path, ev.ByPath)
			if res.Path == "" {
				res.Path = evidence.NormalizeResolvePath(roots.ProjectDir, path)
			}
		}
		// Survey records may verify excerpts against the working tree.
		if res.Verdict == evidence.VerdictUnverifiable &&
			HostVerifyFindingAgainstTree(roots, path, line, excerpt) {
			res.Verdict = evidence.VerdictMatched
		}
		return res
	}
	if handle == "" {
		return evidence.Resolution{
			Line:    line,
			Excerpt: excerpt,
			Verdict: evidence.VerdictUnverifiable,
		}
	}
	rec, ok := evidence.ResolveHandle(ev, handle)
	if !ok {
		return evidence.Resolution{
			Line:    line,
			Excerpt: excerpt,
			Verdict: evidence.VerdictUnverifiable,
		}
	}
	if strings.TrimSpace(rec.Path) == "" {
		return resolveHandleOnlyCitation(ev, handle, line, excerpt)
	}
	res := evidence.Resolve(roots, evidence.Triple{
		Path: rec.Path, Line: line, Excerpt: excerpt,
	}, ev, handle)
	if strings.TrimSpace(res.Path) == "" {
		res.Path = rec.Path
	}
	return res
}

// resolveBareHandleCitation checks direct handle membership.
func resolveBareHandleCitation(ev evidence.Ledger, handle string) (evidence.Resolution, bool) {
	rec, ok := evidence.ResolveHandle(ev, handle)
	if !ok {
		return evidence.Resolution{Handle: handle, Verdict: evidence.VerdictUnverifiable}, false
	}
	return evidence.Resolution{
		Handle:  handle,
		Path:    strings.TrimSpace(rec.Path),
		Verdict: evidence.VerdictTraced,
	}, true
}

func resolveHandleOnlyCitation(ev evidence.Ledger, handle string, line int, excerpt string) evidence.Resolution {
	out := evidence.Resolution{
		Handle:  handle,
		Line:    line,
		Excerpt: excerpt,
	}
	var path string
	if rec, ok := evidence.ResolveHandle(ev, handle); ok {
		path = rec.Path
		out.Path = rec.Path
	}
	if evidence.ExcerptMatchesHandle(ev, handle, path, line, excerpt) {
		out.Verdict = evidence.VerdictMatched
		return out
	}
	if excerpt != "" && excerptMatchesOtherHandle(ev, handle, line, excerpt) {
		out.Verdict = evidence.VerdictUnverifiable
		return out
	}
	out.Verdict = evidence.VerdictTraced
	return out
}

func excerptMatchesOtherHandle(ev evidence.Ledger, citedHandle string, line int, excerpt string) bool {
	excerpt = strings.TrimSpace(excerpt)
	if excerpt == "" {
		return false
	}
	for handle := range ev.Handles {
		if handle == citedHandle {
			continue
		}
		var otherPath string
		if rec, ok := evidence.ResolveHandle(ev, handle); ok {
			otherPath = rec.Path
		}
		if evidence.ExcerptMatchesHandle(ev, handle, otherPath, line, excerpt) {
			return true
		}
	}
	return false
}

// FormatResolutionOffender renders a resolved citation for reject/audit tokens.
func FormatResolutionOffender(res evidence.Resolution, finding WorkerFindingInput) string {
	path := strings.TrimSpace(res.Path)
	if path != "" {
		if res.Line > 0 && strings.TrimSpace(res.Excerpt) != "" {
			return fmt.Sprintf("%s:%d (%q)", path, res.Line, strings.TrimSpace(res.Excerpt))
		}
		if res.Line > 0 {
			return fmt.Sprintf("%s:%d", path, res.Line)
		}
		if strings.TrimSpace(res.Excerpt) != "" {
			return fmt.Sprintf("%s (%q)", path, strings.TrimSpace(res.Excerpt))
		}
		return path
	}
	return formatFindingOffender(finding)
}

func formatFindingOffender(f WorkerFindingInput) string {
	path := strings.TrimSpace(f.Path)
	handle := strings.TrimSpace(f.Evidence)
	if path != "" {
		if f.Line > 0 && strings.TrimSpace(f.Excerpt) != "" {
			return fmt.Sprintf("%s:%d (%q)", path, f.Line, strings.TrimSpace(f.Excerpt))
		}
		if f.Line > 0 {
			return fmt.Sprintf("%s:%d", path, f.Line)
		}
		if strings.TrimSpace(f.Excerpt) != "" {
			return fmt.Sprintf("%s (%q)", path, strings.TrimSpace(f.Excerpt))
		}
		return path
	}
	if f.Line > 0 && strings.TrimSpace(f.Excerpt) != "" {
		return fmt.Sprintf("%s:%d (%q)", handle, f.Line, strings.TrimSpace(f.Excerpt))
	}
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d", handle, f.Line)
	}
	if strings.TrimSpace(f.Excerpt) != "" {
		return fmt.Sprintf("%s (%q)", handle, strings.TrimSpace(f.Excerpt))
	}
	return handle
}

func citedURLHandleOffenders(urls []string, ev evidence.Ledger) []string {
	var offenders []string
	seen := map[string]struct{}{}
	for _, url := range urls {
		url = strings.TrimSpace(url)
		if url == "" {
			continue
		}
		if ev.URLSeen(url) {
			continue
		}
		addOffender(&offenders, seen, url)
	}
	return offenders
}

// GroundingHintData maps offenders and observed evidence to bounded hint template vars.
func GroundingHintData(offenders []string, ev evidence.Ledger) map[string]any {
	data := OffenderHintData(offenders)
	handleReport := FormatOffenderReport(evidence.HandlesSorted(ev))
	data["observed_handles_count"] = handleReport.Count
	data["observed_handles_sample"] = handleReport.Sample
	data["observed_handles_omitted"] = handleReport.Omitted
	pathReport := FormatOffenderReport(evidence.ObservedPathsSorted(ev))
	data["observed_paths_count"] = pathReport.Count
	data["observed_paths_sample"] = pathReport.Sample
	data["observed_paths_omitted"] = pathReport.Omitted
	urlReport := FormatOffenderReport(evidence.ObservedURLsSorted(ev))
	data["observed_urls_count"] = urlReport.Count
	data["observed_urls_sample"] = urlReport.Sample
	data["observed_urls_omitted"] = urlReport.Omitted
	return data
}

// citedHandleRangeLimit bounds the handles a refusal describes.
const citedHandleRangeLimit = 8

// CitedHandleRanges describes the lines each resolved handle observed, for
// citations whose handle exists but whose line or excerpt did not match.
func CitedHandleRanges(resolutions []evidence.Resolution, ev evidence.Ledger) string {
	var parts []string
	seen := map[string]bool{}
	for _, res := range resolutions {
		if res.Verdict != evidence.VerdictUnverifiable || res.Handle == "" || seen[res.Handle] {
			continue
		}
		rec, ok := evidence.ResolveHandle(ev, res.Handle)
		if !ok || len(rec.LineRanges) == 0 {
			continue
		}
		seen[res.Handle] = true
		ranges := make([]string, 0, len(rec.LineRanges))
		for _, r := range rec.LineRanges {
			ranges = append(ranges, fmt.Sprintf("%d-%d", r.Start, r.End))
		}
		parts = append(parts, fmt.Sprintf("%s %s lines %s", res.Handle, rec.Path, strings.Join(ranges, ",")))
		if len(parts) == citedHandleRangeLimit {
			break
		}
	}
	return strings.Join(parts, "; ")
}

// OffenderHintData maps full offender tokens to bounded hint template vars.
func OffenderHintData(offenders []string) map[string]any {
	report := FormatOffenderReport(offenders)
	return map[string]any{
		"offender_count":    report.Count,
		"offenders_sample":  report.Sample,
		"offenders_omitted": report.Omitted,
	}
}

// ResolvedFinding is a handle-resolved finding.
type ResolvedFinding struct {
	Handle  string
	Path    string
	Line    int
	Excerpt string
	Verdict evidence.Verdict
	Note    string
}

// ResolveFindingsForWire maps typed findings to host provenance.
func ResolveFindingsForWire(
	roots evidence.CitationRoots,
	ev evidence.Ledger,
	findings []WorkerFindingInput,
	notes []string,
) []ResolvedFinding {
	var out []ResolvedFinding
	for i, finding := range findings {
		note := ""
		if i < len(notes) {
			note = strings.TrimSpace(notes[i])
		}
		path := strings.TrimSpace(finding.Path)
		handle := strings.TrimSpace(finding.Evidence)
		if path == "" && handle == "" && note == "" {
			continue
		}
		line := finding.Line
		excerpt := strings.TrimSpace(finding.Excerpt)
		if path != "" && line <= 0 && excerpt == "" {
			continue
		}
		res := resolveWorkerFindingCitation(roots, finding, ev)
		if !res.Verdict.Grounded() {
			continue
		}
		out = append(out, ResolvedFinding{
			Handle:  res.Handle,
			Path:    res.Path,
			Line:    res.Line,
			Excerpt: res.Excerpt,
			Verdict: res.Verdict,
			Note:    note,
		})
	}
	return out
}
