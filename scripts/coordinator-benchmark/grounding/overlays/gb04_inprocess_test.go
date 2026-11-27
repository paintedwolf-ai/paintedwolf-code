package guidance_test

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
)

type gb04InProcessCase struct {
	CaseID              string               `json:"case_id"`
	FamilyID            string               `json:"family_id"`
	Selector            string               `json:"selector"`
	Kind                string               `json:"kind"` // "r04_parse" | "resolve"
	ProjectDir          string               `json:"project_dir,omitempty"`
	Markdown            string               `json:"markdown,omitempty"`
	KnownPaths          []string             `json:"known_paths,omitempty"`
	Records             []gb04RecordInput    `json:"records,omitempty"`
	PermittedArtifacts  []string             `json:"permitted_artifacts,omitempty"`
	Citations           []gb04CitationInput  `json:"citations,omitempty"`
}

type gb04LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type gb04RecordInput struct {
	Handle              string          `json:"handle"`
	Kind                string          `json:"kind"`
	Tool                string          `json:"tool"`
	Path                string          `json:"path"`
	URL                 string          `json:"url"`
	ArtifactID          string          `json:"artifact_id"`
	RawText             string          `json:"raw_text"`
	DeliveredRanges     []gb04LineRange `json:"delivered_ranges"`
	LineNumbered        bool            `json:"line_numbered"`
	PrintedOtherNumbers bool            `json:"printed_other_numbers"`
	SupersededBy        string          `json:"superseded_by"`
	ModelVisible        bool            `json:"model_visible"`
}

type gb04CitationInput struct {
	CitationID   string `json:"citation_id"`
	CitationType string `json:"citation_type"` // handle | path_coord | handle_path | url | artifact
	Raw          string `json:"raw"`
	Handle       string `json:"handle"`
	Path         string `json:"path"`
	StartLine    int    `json:"start_line"`
	EndLine      int    `json:"end_line"`
	Excerpt      string `json:"excerpt"`
	URL          string `json:"url"`
	ArtifactID   string `json:"artifact_id"`
}

type gb04R04OccurrenceOut struct {
	Kind      string `json:"kind"`
	SpanStart int    `json:"span_start"`
	SpanEnd   int    `json:"span_end"`
	NodeStart int    `json:"node_start"`
	NodeEnd   int    `json:"node_end"`
	Token     string `json:"token"`
}

type gb04R04IssueOut struct {
	Code      string `json:"code"`
	Kind      string `json:"kind"`
	SpanStart int    `json:"span_start"`
	SpanEnd   int    `json:"span_end"`
	Token     string `json:"token"`
}

type gb04CitationResultOut struct {
	CitationID        string `json:"citation_id"`
	Outcome           string `json:"outcome"`
	Verdict           string `json:"verdict"`
	Grounded          bool   `json:"grounded"`
	ResolvedHandle    string `json:"resolved_handle"`
	ResolvedPath      string `json:"resolved_path"`
	ResolvedLine      int    `json:"resolved_line"`
	ResolvedEndLine   int    `json:"resolved_end_line"`
	LineCorrectedFrom int    `json:"line_corrected_from"`
	Code              string `json:"code"`
	Reason            string `json:"reason"`
}

type gb04InProcessResult struct {
	CaseID      string                  `json:"case_id"`
	FamilyID    string                  `json:"family_id"`
	Selector    string                  `json:"selector"`
	Kind        string                  `json:"kind"`
	Occurrences []gb04R04OccurrenceOut  `json:"occurrences,omitempty"`
	Issues      []gb04R04IssueOut       `json:"issues,omitempty"`
	Resolutions []gb04CitationResultOut `json:"resolutions,omitempty"`
	Error       string                  `json:"error,omitempty"`
}

func buildGB04Ledger(inputs []gb04RecordInput) evidence.Ledger {
	recs := make([]evidence.Record, 0, len(inputs))
	for i, in := range inputs {
		if !in.ModelVisible {
			continue
		}
		rec := evidence.Record{
			Handle:       in.Handle,
			Kind:         in.Kind,
			Fidelity:     evidence.FidelityStructured,
			SourceTool:   in.Tool,
			SupersededBy: in.SupersededBy,
			Path:         in.Path,
			URL:          in.URL,
			ArtifactID:   in.ArtifactID,
		}
		switch {
		case in.ArtifactID != "" || in.Kind == "artifact":
			rec.Shape = evidence.ShapeArtifact
		case in.URL != "" && in.Kind != "dialed_host":
			rec.Shape = evidence.ShapeURL
		case in.Kind == "dialed_host":
			rec.Shape = evidence.ShapeOpaque
		default:
			rec.Shape = evidence.ShapeFileRegion
		}
		for _, r := range in.DeliveredRanges {
			rec.LineRanges = append(rec.LineRanges, evidence.LineRange{Start: r.Start, End: r.End})
		}
		trimmed := strings.TrimSuffix(strings.ReplaceAll(in.RawText, "\r\n", "\n"), "\n")
		var lines []string
		if trimmed != "" {
			lines = strings.Split(trimmed, "\n")
		}
		rec.Body = lines
		if (!in.LineNumbered || in.PrintedOtherNumbers) && len(in.DeliveredRanges) > 0 {
			rng := in.DeliveredRanges[0]
			rec.SourceDelivery = &evidence.SourceDelivery{
				ResponseID:          "gb04-resp",
				DeliveryID:          "gb04-deliv-" + in.Handle,
				StartLine:           rng.Start,
				EndLine:             rng.End,
				LineNumbered:        in.LineNumbered,
				PrintedOtherNumbers: in.PrintedOtherNumbers,
			}
		}
		rec.SetCustodySeq(i + 1)
		recs = append(recs, rec)
	}
	return evidence.AssembleLedger(recs)
}

func runGB04R04Case(c gb04InProcessCase) gb04InProcessResult {
	ev := evidence.InitLedger()
	knownPaths := make(map[string]struct{}, len(c.KnownPaths))
	for _, p := range c.KnownPaths {
		knownPaths[p] = struct{}{}
	}
	opts := citationmd.Options{
		KnownHandleKind: guidance.KnownEvidenceKind(ev),
	}
	if len(knownPaths) > 0 {
		opts.KnownPath = func(name string) bool {
			_, ok := knownPaths[name]
			return ok
		}
	}
	doc := citationmd.Parse(c.Markdown, opts)
	out := gb04InProcessResult{
		CaseID:      c.CaseID,
		FamilyID:    c.FamilyID,
		Selector:    c.Selector,
		Kind:        c.Kind,
		Occurrences: make([]gb04R04OccurrenceOut, 0, len(doc.Occurrences)),
		Issues:      make([]gb04R04IssueOut, 0, len(doc.Issues)),
	}
	for _, occ := range doc.Occurrences {
		out.Occurrences = append(out.Occurrences, gb04R04OccurrenceOut{
			Kind:      string(occ.Kind),
			SpanStart: occ.Span.Start,
			SpanEnd:   occ.Span.End,
			NodeStart: occ.Node.Start,
			NodeEnd:   occ.Node.End,
			Token:     occ.Token,
		})
	}
	for _, iss := range doc.Issues {
		out.Issues = append(out.Issues, gb04R04IssueOut{
			Code:      string(iss.Code),
			Kind:      string(iss.Kind),
			SpanStart: iss.Span.Start,
			SpanEnd:   iss.Span.End,
			Token:     iss.Token,
		})
	}
	return out
}

func runGB04ResolveCase(c gb04InProcessCase) gb04InProcessResult {
	ev := buildGB04Ledger(c.Records)
	projectDir := c.ProjectDir
	if projectDir == "" {
		projectDir = "/tmp/gb04-inprocess"
	}
	roots := evidence.CitationRoots{ProjectDir: projectDir}
	draft := guidance.CloseoutDraft{
		Occurrences: make([]guidance.DraftOccurrence, 0, len(c.Citations)),
	}
	for idx, cit := range c.Citations {
		occ := citationmd.Occurrence{
			Index: idx,
			Token: cit.Raw,
		}
		switch cit.CitationType {
		case "url":
			occ.Kind = citationmd.KindWebLink
			occ.Link = &citationmd.Link{
				Form:        citationmd.LinkAutolink,
				Label:       cit.URL,
				Destination: cit.URL,
			}
		case "artifact":
			occ.Kind = citationmd.KindArtifact
			occ.Artifact = cit.ArtifactID
		default:
			occ.Kind = citationmd.KindCitation
			path := strings.Trim(cit.Path, `"`)
			occ.Citation = &citationmd.Citation{
				Handle:  cit.Handle,
				Path:    path,
				Line:    cit.StartLine,
				EndLine: cit.EndLine,
				Excerpt: cit.Excerpt,
			}
		}
		draft.Occurrences = append(draft.Occurrences, guidance.DraftOccurrence{
			Occurrence: occ,
			Owner:      guidance.DraftFieldSynthesis,
		})
	}

	resolved := guidance.ResolveDraftCitations(
		roots,
		"implement_synthesis",
		draft,
		guidance.CloseoutEvidence{Ledger: ev},
		c.PermittedArtifacts,
	)

	out := gb04InProcessResult{
		CaseID:      c.CaseID,
		FamilyID:    c.FamilyID,
		Selector:    c.Selector,
		Kind:        c.Kind,
		Resolutions: make([]gb04CitationResultOut, 0, len(resolved.Occurrences)),
	}
	for i, occRes := range resolved.Occurrences {
		cid := ""
		if i < len(c.Citations) {
			cid = c.Citations[i].CitationID
		}
		verdict := string(occRes.Resolution.Verdict)
		grounded := !occRes.Rejected() && (occRes.Outcome == guidance.CitationGrounded || occRes.Outcome == guidance.ArtifactPresentable)
		if verdict == "" {
			if grounded {
				verdict = string(evidence.VerdictMatched)
			} else {
				verdict = string(evidence.VerdictUnverifiable)
			}
		}
		out.Resolutions = append(out.Resolutions, gb04CitationResultOut{
			CitationID:        cid,
			Outcome:           string(occRes.Outcome),
			Verdict:           verdict,
			Grounded:          grounded,
			ResolvedHandle:    occRes.Resolution.Handle,
			ResolvedPath:      occRes.Resolution.Path,
			ResolvedLine:      occRes.Resolution.Line,
			ResolvedEndLine:   occRes.Resolution.EndLine,
			LineCorrectedFrom: occRes.Resolution.LineCorrectedFrom,
			Code:              occRes.Code,
			Reason:            occRes.Resolution.Reason,
		})
	}
	return out
}

// TestGB04InProcess executes the GB04 in-process JSONL batch against D1's real
// citationmd parser and guidance/evidence citation resolver.
func TestGB04InProcess(t *testing.T) {
	casesPath := strings.TrimSpace(os.Getenv("GB04_CASES_FILE"))
	resultsPath := strings.TrimSpace(os.Getenv("GB04_RESULTS_FILE"))
	if casesPath == "" || resultsPath == "" {
		t.Skip("GB04_CASES_FILE and GB04_RESULTS_FILE not set")
	}

	inFile, err := os.Open(casesPath)
	if err != nil {
		t.Fatalf("open cases file: %v", err)
	}
	defer inFile.Close()

	outFile, err := os.Create(resultsPath)
	if err != nil {
		t.Fatalf("create results file: %v", err)
	}
	defer outFile.Close()

	scanner := bufio.NewScanner(inFile)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	writer := bufio.NewWriter(outFile)
	defer writer.Flush()

	count := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var c gb04InProcessCase
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("unmarshal case line %d: %v", count+1, err)
		}
		var res gb04InProcessResult
		switch c.Kind {
		case "r04_parse":
			res = runGB04R04Case(c)
		case "resolve":
			res = runGB04ResolveCase(c)
		default:
			res = gb04InProcessResult{
				CaseID:   c.CaseID,
				FamilyID: c.FamilyID,
				Selector: c.Selector,
				Kind:     c.Kind,
				Error:    "unknown case kind: " + c.Kind,
			}
		}
		encoded, err := json.Marshal(res)
		if err != nil {
			t.Fatalf("marshal result %s: %v", c.CaseID, err)
		}
		if _, err := writer.Write(encoded); err != nil {
			t.Fatalf("write result %s: %v", c.CaseID, err)
		}
		if err := writer.WriteByte('\n'); err != nil {
			t.Fatalf("write newline %s: %v", c.CaseID, err)
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan cases file: %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush results file: %v", err)
	}
	t.Logf("GB04 in-process executed %d cases", count)
}
