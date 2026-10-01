package search

import (
	"strings"

	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProjectMessage projects one persisted message into evidence_index rows from grounding.
func ProjectMessage(projectID, sessionID string, msg api.Message) []IndexRow {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil
	}
	ts := timelayout.Format(msg.CreatedAt)
	role := messageRole(msg)
	var rows []IndexRow
	rows = append(rows, groundingRows(projectID, sessionID, msg.ID, ts, role, msg.Grounding, "")...)
	// Worker-summary grounding contributes evidence and findings to transcript search.
	if ws := msg.WorkerSummary; ws != nil {
		rows = append(rows, groundingRows(projectID, sessionID, msg.ID, ts, role, ws.Grounding, "ws_")...)
	}
	return rows
}

// groundingRows projects evidence with distinct row IDs for each grounding source.
func groundingRows(projectID, sessionID, messageID, ts, role string, gr *api.CitationGrounding, prefix string) []IndexRow {
	if gr == nil {
		return nil
	}
	verified := gr.Traced
	var rows []IndexRow
	for i, rec := range gr.EvidenceRecords {
		rows = append(rows, evidenceRecordRow(projectID, sessionID, messageID, ts, role, verified, gr.HintCode, rec, rowSuffix(prefix+"evidence_record", i, rec.Handle)))
	}
	for i, cited := range gr.CitedEvidence {
		rows = append(rows, citedEvidenceRow(projectID, sessionID, messageID, ts, role, gr.HintCode, cited, rowSuffix(prefix+"cited", i, cited.Handle)))
	}
	for i, finding := range gr.Findings {
		rows = append(rows, findingRow(projectID, sessionID, messageID, ts, role, gr.HintCode, finding, rowSuffix(prefix+"finding", i, finding.Handle)))
	}
	return rows
}

func messageRole(msg api.Message) string {
	if msg.Kind == api.MessageKindDraft &&
		msg.DraftStatus == api.DraftStatusCommitted && len(msg.ToolCalls) == 0 {
		return string(api.MessageRoleAssistant)
	}
	if k := strings.TrimSpace(string(msg.Kind)); k != "" {
		return k
	}
	return string(msg.Role)
}

func rowSuffix(kind string, idx int, handle string) string {
	if h := strings.TrimSpace(handle); h != "" {
		return kind + ":" + h
	}
	return kind + ":" + itoa(idx)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func evidenceRecordRow(projectID, sessionID, messageID, ts, role string, verified bool, hintCode string, rec api.CitationGroundingEvidenceRecord, suffix string) IndexRow {
	kind := strings.TrimSpace(rec.Kind)
	if kind == "" {
		kind = HitKindEvidence
	}
	url := firstURL(rec.URLs)
	v := verified
	return IndexRow{
		ID:        RowID(SourceMessage, messageID, suffix),
		ProjectID: projectID,
		Source:    SourceMessage,
		HitKind:   HitKindEvidence,
		SessionID: sessionID,
		MessageID: messageID,
		SourceRef: messageID,
		Handle:    rec.Handle,
		Kind:      kind,
		Shape:     rec.Shape,
		Role:      role,
		Path:      rec.Path,
		Line:      rec.Line,
		URL:       url,
		Snippet:   rec.Excerpt,
		Verified:  &v,
		HintCode:  hintCode,
		Trust:     rec.Fidelity,
		Truncated: rec.Truncated,
		TS:        ts,
		Untrusted: ingestion.IsRetrievalTool(rec.Tool),
	}
}

func citedEvidenceRow(projectID, sessionID, messageID, ts, role, hintCode string, cited api.CitationGroundingCitedEvidence, suffix string) IndexRow {
	v := verdictVerified(cited.Verdict)
	return IndexRow{
		ID:        RowID(SourceMessage, messageID, suffix),
		ProjectID: projectID,
		Source:    SourceMessage,
		HitKind:   HitKindEvidence,
		SessionID: sessionID,
		MessageID: messageID,
		SourceRef: messageID,
		Handle:    cited.Handle,
		Kind:      HitKindEvidence,
		Role:      role,
		Path:      cited.Path,
		Line:      cited.Line,
		Snippet:   cited.Excerpt,
		Verified:  &v,
		HintCode:  hintCode,
		TS:        ts,
	}
}

func findingRow(projectID, sessionID, messageID, ts, role, hintCode string, finding api.CitationGroundingFinding, suffix string) IndexRow {
	v := verdictVerified(finding.Verdict)
	return IndexRow{
		ID:        RowID(SourceMessage, messageID, suffix),
		ProjectID: projectID,
		Source:    SourceMessage,
		HitKind:   HitKindClaim,
		SessionID: sessionID,
		MessageID: messageID,
		SourceRef: messageID,
		Handle:    finding.Handle,
		Kind:      HitKindClaim,
		Role:      role,
		Path:      finding.Path,
		Line:      finding.Line,
		Snippet:   firstNonEmpty(finding.Excerpt, finding.Note),
		Verified:  &v,
		HintCode:  hintCode,
		TS:        ts,
	}
}

func verdictVerified(v api.CitationVerdict) bool {
	switch v {
	case api.CitationVerdictMatched, api.CitationVerdictTraced:
		return true
	default:
		return false
	}
}

func firstURL(urls []string) string {
	for _, u := range urls {
		if u = strings.TrimSpace(u); u != "" {
			return u
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
