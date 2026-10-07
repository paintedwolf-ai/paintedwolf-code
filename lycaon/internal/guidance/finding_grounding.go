package guidance

import (
	"github.com/lycaon/lycaon/internal/evidence"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const FindingUngroundedCode = "FINDING_UNGROUNDED"

// TypedCitation is one typed citation from a record_finding ref.
type TypedCitation struct {
	Path    string
	Handle  string
	Line    int
	Excerpt string
	URL     string
	Raw     string
}

// FindingGroundingEval is the outcome of record_finding evidence grounding.
// Typed citation failures warn or block per grounding.yaml. Prose leaks are advisory:
// observed tokens in narrative never trigger FINDING_UNGROUNDED.
type FindingGroundingEval struct {
	Code                 string
	Offenders            []string
	Grounded             bool
	ProseDuplicateTokens []string
	ProseDuplicateCount  int
	ProseAdvisoryTokens  []string
	ProseAdvisoryCount   int
}

var findingHandleLineRE = regexp.MustCompile(`^((?:[a-zA-Z0-9_-]+:)?[a-z]+#[1-9][0-9]*)(?::(\d+))?(?:\s+(.*))?$`)

// EvaluateFindingSummary checks one record_finding summary against the ledger.
func EvaluateFindingSummary(projectDir, summary, ref string, ev evidence.Ledger) FindingGroundingEval {
	var typed []TypedCitation
	if evidenceRef := noteEvidenceTypedCitation(ref); len(evidenceRef) > 0 {
		typed = evidenceRef
	}
	return evaluateNarrative(projectDir, strings.TrimSpace(summary), typed, ev)
}

func noteEvidenceTypedCitation(ref string) []TypedCitation {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	if evidence.HandleGrammar.MatchString(ref) {
		return parseCitationToken(ref)
	}
	if _, _, ok := evidence.SplitPathLineToken(ref); ok {
		return parseCitationToken(ref)
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return parseCitationToken(ref)
	}
	return nil
}

func evaluateNarrative(projectDir, narrative string, typed []TypedCitation, ev evidence.Ledger) FindingGroundingEval {
	var offenders []string
	seen := map[string]struct{}{}

	for _, cite := range typed {
		if !typedCitationGrounded(projectDir, cite, ev) {
			raw := strings.TrimSpace(cite.Raw)
			if raw == "" {
				raw = citationDisplay(cite)
			}
			addOffender(&offenders, seen, raw)
		}
	}

	ch := BuildTypedChannel(projectDir, ev, typed)
	proseEval := EvaluateWorkerProseLeaks(WorkerNarrativeInput{Brief: narrative}, ev, ch)

	if len(offenders) > 0 {
		return FindingGroundingEval{
			Code:                 FindingUngroundedCode,
			Offenders:            offenders,
			ProseDuplicateTokens: append([]string(nil), proseEval.DuplicateTokens...),
			ProseDuplicateCount:  len(proseEval.DuplicateTokens),
			ProseAdvisoryTokens:  append([]string(nil), proseEval.AdvisoryLeakTokens...),
			ProseAdvisoryCount:   len(proseEval.AdvisoryLeakTokens),
		}
	}
	return FindingGroundingEval{
		Grounded:             true,
		ProseDuplicateTokens: append([]string(nil), proseEval.DuplicateTokens...),
		ProseDuplicateCount:  len(proseEval.DuplicateTokens),
		ProseAdvisoryTokens:  append([]string(nil), proseEval.AdvisoryLeakTokens...),
		ProseAdvisoryCount:   len(proseEval.AdvisoryLeakTokens),
	}
}

// BuildTypedChannel collects typed citation tokens for prose leak membership tests.
func BuildTypedChannel(projectDir string, ev evidence.Ledger, typed []TypedCitation) TypedChannel {
	ch := TypedChannel{
		Paths:     map[string]struct{}{},
		PathLines: map[string]struct{}{},
		URLs:      map[string]struct{}{},
	}
	for _, cite := range typed {
		if url := strings.TrimSpace(cite.URL); url != "" {
			ch.URLs[url] = struct{}{}
		}
		path := strings.TrimSpace(cite.Path)
		if path == "" && strings.TrimSpace(cite.Handle) != "" {
			if rec, ok := evidence.ResolveHandle(ev, cite.Handle); ok {
				path = rec.Path
			}
		}
		if path != "" {
			if rel := normalizedReportPath(evidence.CitationRoots{ProjectDir: projectDir}, path, nil); rel != "" {
				path = rel
			} else {
				path = filepath.ToSlash(path)
			}
			ch.Paths[path] = struct{}{}
			if cite.Line > 0 {
				ch.PathLines[pathLineKey(path, cite.Line)] = struct{}{}
			}
		}
		if handle := strings.TrimSpace(cite.Handle); handle != "" {
			if rec, ok := evidence.ResolveHandle(ev, handle); ok && strings.TrimSpace(rec.Path) != "" {
				path = filepath.ToSlash(rec.Path)
				ch.Paths[path] = struct{}{}
				if cite.Line > 0 {
					ch.PathLines[pathLineKey(path, cite.Line)] = struct{}{}
				}
			}
		}
	}
	return ch
}

func typedCitationGrounded(projectDir string, cite TypedCitation, ev evidence.Ledger) bool {
	handle := strings.TrimSpace(cite.Handle)
	path := strings.TrimSpace(cite.Path)
	line := cite.Line
	excerpt := strings.TrimSpace(cite.Excerpt)
	if url := strings.TrimSpace(cite.URL); url != "" {
		return evidence.VerifyURLObserved(ev, url)
	}
	if handle != "" {
		if line > 0 || excerpt != "" {
			p := path
			if p == "" {
				if rec, ok := evidence.ResolveHandle(ev, handle); ok {
					p = rec.Path
				}
			}
			return evidence.ExcerptMatchesHandle(ev, handle, p, line, excerpt)
		}
		_, ok := evidence.ResolveHandle(ev, handle)
		return ok
	}
	if path == "" {
		return true
	}
	if pathPart, ln, ok := evidence.SplitPathLineToken(path); ok {
		path = pathPart
		if line <= 0 {
			line = ln
		}
	}
	res := evidence.Resolve(evidence.CitationRoots{ProjectDir: projectDir}, evidence.Triple{
		Path: path, Line: line, Excerpt: excerpt,
	}, ev, "")
	return res.Verdict != evidence.VerdictUnverifiable
}

func citationDisplay(cite TypedCitation) string {
	if cite.Handle != "" {
		if cite.Line > 0 {
			return cite.Handle + ":" + strconv.Itoa(cite.Line)
		}
		return cite.Handle
	}
	if cite.Path != "" && cite.Line > 0 {
		return cite.Path + ":" + strconv.Itoa(cite.Line)
	}
	return cite.Path
}

func parseCitationToken(token string) []TypedCitation {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	if strings.HasPrefix(token, "http://") || strings.HasPrefix(token, "https://") {
		return []TypedCitation{{URL: token, Raw: token}}
	}
	if m := findingHandleLineRE.FindStringSubmatch(token); m != nil {
		cite := TypedCitation{Handle: m[1], Raw: token}
		if m[2] != "" {
			if n, err := strconv.Atoi(m[2]); err == nil {
				cite.Line = n
			}
		}
		if m[3] != "" {
			cite.Excerpt = strings.Trim(strings.TrimSpace(m[3]), `"'`)
		}
		return []TypedCitation{cite}
	}
	if evidence.HandleGrammar.MatchString(token) {
		return []TypedCitation{{Handle: token, Raw: token}}
	}
	if path, line, ok := evidence.SplitPathLineToken(token); ok {
		return []TypedCitation{{Path: path, Line: line, Raw: token}}
	}
	if norm, ok := evidence.NormalizeCitationPath("", token); ok {
		return []TypedCitation{{Path: norm, Raw: token}}
	}
	return []TypedCitation{{Path: filepath.ToSlash(token), Raw: token}}
}
