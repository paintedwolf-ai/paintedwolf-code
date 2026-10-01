package reporting

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// SurfaceNoteTool is the mid-turn grounded note tool name.
const SurfaceNoteTool = "surface_note"

const (
	surfaceNoteSummaryMaxChars  = 600
	surfaceNoteMaxCitedEvidence = 32
	surfaceNoteMaxCitedURLs     = 16
	surfaceNoteMaxArtifactIDs   = 4
	surfaceNoteWorkerSession    = "SURFACE_NOTE_WORKER_SESSION"
	surfaceNoteUngrounded       = "SURFACE_NOTE_UNGROUNDED"
	surfaceNoteHandleUnground   = "SURFACE_NOTE_HANDLE_UNGROUND"
	surfaceNoteURLOnlyUnground  = "SURFACE_NOTE_URL_ONLY_UNGROUND"
	surfaceNoteInvalidArgs      = "SURFACE_NOTE_INVALID_ARGS"
	surfaceNoteArtifactUnknown  = "SURFACE_NOTE_ARTIFACT_UNKNOWN"
)

// surfaceNoteFrictionCodes spend the shared grounding budget.
var surfaceNoteFrictionCodes = map[string]bool{
	surfaceNoteUngrounded:      true,
	surfaceNoteHandleUnground:  true,
	surfaceNoteURLOnlyUnground: true,
}

// SurfaceNoteMessages loads the session transcript for closeout evidence union.
type SurfaceNoteMessages func(ctx context.Context, sessionID string) ([]api.Message, error)

// SurfaceNoteFriction records grounding friction for note rejects that consume the budget.
type SurfaceNoteFriction func(ctx context.Context, sessionID, code string)

// SurfaceNoteDeps wires host seams surface_note needs beyond ToolContext.
type SurfaceNoteDeps struct {
	Ledger   guidance.CloseoutEvidenceReader
	Messages SurfaceNoteMessages
	Friction SurfaceNoteFriction
}

func RunSurfaceNote(ctx context.Context, args map[string]any, tctx tools.ToolContext, deps SurfaceNoteDeps) (string, error) {
	if tools.OutOfSessionScope(SurfaceNoteTool, tctx) {
		return "", &tools.ToolReject{Code: surfaceNoteWorkerSession, Data: map[string]any{}}
	}
	if tctx.Out == nil {
		return "", fmt.Errorf("tool output required")
	}
	summary, citedEvidence, citedURLs, artifactIDs, err := parseSurfaceNoteArgs(args)
	if err != nil {
		return "", err
	}

	sessionID := strings.TrimSpace(tctx.SessionID)
	if sessionID == "" {
		return "", fmt.Errorf("session required")
	}
	history, err := deps.Messages(ctx, sessionID)
	if err != nil {
		return "", fmt.Errorf("load messages: %w", err)
	}
	ev, err := guidance.UnionCloseoutEvidence(ctx, deps.Ledger, sessionID, history)
	if err != nil {
		return "", fmt.Errorf("union closeout evidence: %w", err)
	}

	roots := evidence.CitationRoots{
		ProjectDir:   tctx.ActiveRootPath(),
		Roots:        tctx.Roots,
		ActiveRootID: tctx.ActiveRootID,
	}
	surfaceID := strings.TrimSpace(tctx.TurnSurfaceID)
	report := guidance.CoordinatorCompletionReport{
		Synthesis:     summary,
		CitedEvidence: citedEvidence,
		CitedURLs:     citedURLs,
	}
	report.Normalize()

	// A note presents only stills this turn produced, like its citations.
	if unknown := unpresentableArtifactIDs(artifactIDs, history); len(unknown) > 0 {
		return "", &tools.ToolReject{Code: surfaceNoteArtifactUnknown, Data: map[string]any{
			"artifact_ids":            unknown,
			"surface_presentable_ids": guidance.PresentableVisualArtifactIDs(history),
		}}
	}

	grounding, rejectCode, rejectData := auditSurfaceNoteGrounding(roots, surfaceID, report, ev)
	if rejectCode != "" {
		if surfaceNoteFrictionCodes[rejectCode] && deps.Friction != nil {
			deps.Friction(ctx, sessionID, rejectCode)
		}
		return "", &tools.ToolReject{Code: rejectCode, Data: rejectData}
	}

	noteID := uuid.NewString()
	tctx.Out.AgentNote = &tools.AgentNoteCapture{
		SourceContext: sourceref.Mentioned(tctx.ModelSourceContext, summary),
		MessageID:     noteID,
		Content:       summary,
		Grounding:     grounding,
		ArtifactIDs:   artifactIDs,
	}
	raw, _ := surveyjson.Marshal(map[string]any{
		"status":     "noted",
		"message_id": noteID,
	})
	return string(raw), nil
}

func auditSurfaceNoteGrounding(
	roots evidence.CitationRoots,
	surfaceID string,
	report guidance.CoordinatorCompletionReport,
	ev guidance.CloseoutEvidence,
) (*api.CitationGrounding, string, map[string]any) {
	emptyCitations := guidance.TypedCitationChannelsEmpty(len(report.CitedEvidence), len(report.CitedURLs))
	if emptyCitations {
		return nil, surfaceNoteUngrounded, map[string]any{}
	}

	if len(report.CitedEvidence) == 0 && !guidance.CloseoutLedgerHasCitableEvidence(ev) {
		// URL-only notes require an in-session observation.
		return nil, surfaceNoteURLOnlyUnground, map[string]any{}
	}

	eval := guidance.EvaluateCloseoutCitations(roots, surfaceID, report, ev)
	var offenders []string
	if strings.TrimSpace(eval.Code) != "" {
		offenders = append(offenders, eval.Offenders...)
	}
	offenders = append(offenders, eval.SurveyAdvisoryTokens...)
	if len(offenders) > 0 {
		return nil, surfaceNoteHandleUnground, guidance.GroundingHintData(offenders, ev.Ledger)
	}
	return guidance.BuildCloseoutCitationGrounding(roots, surfaceID, report, ev, eval), "", nil
}

func parseSurfaceNoteArgs(args map[string]any) (
	summary string,
	cited []guidance.CoordinatorCitedEvidence,
	urls []string,
	artifactIDs []string,
	err error,
) {
	invalid := &tools.ToolReject{Code: surfaceNoteInvalidArgs, Data: map[string]any{"field": "summary", "reason": "must be nonempty within the character bound", "max": surfaceNoteSummaryMaxChars}}
	if args == nil {
		return "", nil, nil, nil, invalid
	}
	rawSummary, _ := args["summary"].(string)
	summary = strings.TrimSpace(rawSummary)
	if summary == "" || utf8.RuneCountInString(summary) > surfaceNoteSummaryMaxChars {
		return "", nil, nil, nil, invalid
	}
	cited, err = parseSurfaceNoteCitedEvidence(args["cited_evidence"])
	if err != nil {
		return "", nil, nil, nil, err
	}
	urls, err = parseSurfaceNoteCitedURLs(args["cited_urls"])
	if err != nil {
		return "", nil, nil, nil, err
	}
	artifactIDs, err = parseSurfaceNoteArtifactIDs(args["artifact_ids"])
	if err != nil {
		return "", nil, nil, nil, err
	}
	return summary, cited, urls, artifactIDs, nil
}

func surfaceNoteStringList(raw any, max int, field string) ([]string, error) {
	invalid := &tools.ToolReject{Code: surfaceNoteInvalidArgs, Data: map[string]any{"field": field, "reason": "must be an array of nonempty strings within the item bound", "max": max}}
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, invalid
	}
	if len(items) > max {
		return nil, invalid
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, invalid
		}
		if s = strings.TrimSpace(s); s == "" {
			return nil, invalid
		}
		out = append(out, s)
	}
	return out, nil
}

func parseSurfaceNoteArtifactIDs(raw any) ([]string, error) {
	ids, err := surfaceNoteStringList(raw, surfaceNoteMaxArtifactIDs, "artifact_ids")
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

// unpresentableArtifactIDs returns the requested ids the turn never observed.
func unpresentableArtifactIDs(requested []string, history []api.Message) []string {
	if len(requested) == 0 {
		return nil
	}
	allowed := make(map[string]struct{})
	for _, id := range guidance.PresentableVisualArtifactIDs(history) {
		allowed[id] = struct{}{}
	}
	var unknown []string
	for _, id := range requested {
		if _, ok := allowed[id]; !ok {
			unknown = append(unknown, id)
		}
	}
	return unknown
}

func parseSurfaceNoteCitedEvidence(raw any) ([]guidance.CoordinatorCitedEvidence, error) {
	items, err := surfaceNoteStringList(raw, surfaceNoteMaxCitedEvidence, "cited_evidence")
	if err != nil {
		return nil, err
	}
	out := make([]guidance.CoordinatorCitedEvidence, 0, len(items))
	for _, s := range items {
		if token, line, ok := evidence.SplitPathLineToken(s); ok {
			if evidence.IsEvidenceHandleToken(token) {
				out = append(out, guidance.CoordinatorCitedEvidence{Evidence: token, Line: line})
			} else {
				out = append(out, guidance.CoordinatorCitedEvidence{Path: token, Line: line})
			}
			continue
		}
		if evidence.IsEvidenceHandleToken(s) {
			out = append(out, guidance.CoordinatorCitedEvidence{Evidence: s})
			continue
		}
		out = append(out, guidance.CoordinatorCitedEvidence{Path: s})
	}
	return out, nil
}

func parseSurfaceNoteCitedURLs(raw any) ([]string, error) {
	items, err := surfaceNoteStringList(raw, surfaceNoteMaxCitedURLs, "cited_urls")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, s := range items {
		u, err := url.Parse(s)
		if err != nil || u.Scheme == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, &tools.ToolReject{Code: surfaceNoteInvalidArgs, Data: map[string]any{"field": "cited_urls", "reason": "must contain absolute HTTP(S) URLs"}}
		}
		out = append(out, s)
	}
	return out, nil
}
