package search

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

const scanKind = "scan"
const scanShape = "artifact"

// ProjectScanFindings projects capped scan findings into evidence_index rows.
func ProjectScanFindings(projectID, scanID string, findings []api.SecurityFinding, ts string) []IndexRow {
	projectID = strings.TrimSpace(projectID)
	scanID = strings.TrimSpace(scanID)
	if projectID == "" || scanID == "" {
		return nil
	}
	out := make([]IndexRow, 0, len(findings))
	for _, f := range findings {
		fp := strings.TrimSpace(f.Fingerprints.Primary)
		if fp == "" {
			fp = "missing"
		}
		out = append(out, IndexRow{
			ID:        RowID(SourceFinding, scanID, fp),
			ProjectID: projectID,
			Source:    SourceFinding,
			HitKind:   HitKindFinding,
			SourceRef: scanID,
			Handle:    fp,
			Kind:      scanKind,
			Shape:     scanShape,
			Path:      primaryURI(f),
			Line:      primaryLine(f),
			Snippet:   firstNonEmpty(f.Message, f.RuleID),
			HintCode:  hintCodeFor(f),
			Trust:     string(f.Level),
			TS:        ts,
		})
	}
	return out
}

func primaryURI(f api.SecurityFinding) string {
	if len(f.Locations) == 0 {
		return ""
	}
	return f.Locations[0].URI
}

func primaryLine(f api.SecurityFinding) int {
	if len(f.Locations) == 0 {
		return 0
	}
	return f.Locations[0].StartLine
}

func hintCodeFor(f api.SecurityFinding) string {
	if f.Properties != nil && f.Properties.Lycaon != nil {
		return strings.TrimSpace(f.Properties.Lycaon.HintCode)
	}
	return ""
}
