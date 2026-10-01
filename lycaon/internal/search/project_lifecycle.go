package search

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/internal/workercompletionxml"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProjectLifecycleEvidence indexes searchable lifecycle records.
func ProjectLifecycleEvidence(projectID, sessionID string, msg api.Message) []IndexRow {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil
	}
	ts := timelayout.Format(msg.CreatedAt)
	role := messageRole(msg)
	var rows []IndexRow

	if api.IsWorkflowBoundaryMessage(msg) && msg.WorkflowBoundary != nil {
		event := strings.TrimSpace(msg.WorkflowBoundary.Event)
		snippet := strings.TrimSpace(msg.WorkflowBoundary.Reason)
		if snippet == "" {
			snippet = event
		}
		if phase := strings.TrimSpace(msg.WorkflowBoundary.Phase); phase != "" {
			snippet = event + " · " + phase
		}
		rows = append(rows, IndexRow{
			ID:        RowID(SourceMessage, msg.ID, "boundary:"+event),
			ProjectID: projectID,
			Source:    SourceMessage,
			HitKind:   HitKindMessage,
			SessionID: sessionID,
			MessageID: msg.ID,
			SourceRef: msg.ID,
			Kind:      "workflow_boundary",
			Role:      role,
			Snippet:   truncateSnippet(snippet, 2000),
			TS:        ts,
		})
	}

	if msg.ToolResult != nil && msg.ToolResult.CheckpointDecision != nil {
		dec := msg.ToolResult.CheckpointDecision
		parts := []string{string(dec.Status), string(dec.Kind)}
		if tool := strings.TrimSpace(dec.Tool); tool != "" {
			parts = append(parts, tool)
		}
		if subject := strings.TrimSpace(dec.Subject); subject != "" {
			parts = append(parts, subject)
		}
		if title := strings.TrimSpace(dec.GrantTitle); title != "" {
			parts = append(parts, title)
		}
		snippet := strings.Join(parts, " ")
		rows = append(rows, IndexRow{
			ID:        RowID(SourceMessage, msg.ID, "checkpoint:"+dec.CheckpointID),
			ProjectID: projectID,
			Source:    SourceMessage,
			HitKind:   HitKindMessage,
			SessionID: sessionID,
			MessageID: msg.ID,
			SourceRef: msg.ID,
			Kind:      "checkpoint_decision",
			Role:      role,
			Snippet:   snippet,
			TS:        ts,
		})
	}

	if api.IsWorkflowFeedbackMessage(msg) && msg.WorkflowFeedback != nil {
		answer := strings.TrimSpace(msg.WorkflowFeedback.Answer)
		if answer == "" {
			answer = strings.TrimSpace(msg.Content)
		}
		if answer != "" {
			rows = append(rows, IndexRow{
				ID:        RowID(SourceMessage, msg.ID, "workflow_feedback"),
				ProjectID: projectID,
				Source:    SourceMessage,
				HitKind:   HitKindMessage,
				SessionID: sessionID,
				MessageID: msg.ID,
				SourceRef: msg.ID,
				Kind:      "workflow_feedback",
				Role:      role,
				Snippet:   truncateSnippet(answer, 2000),
				TS:        ts,
			})
		}
	}

	if api.IsBlueprintMessage(msg) {
		snippet := strings.TrimSpace(msg.Content)
		if msg.Blueprint != nil && strings.TrimSpace(msg.Blueprint.BlueprintTitle) != "" {
			snippet = strings.TrimSpace(msg.Blueprint.BlueprintTitle) + ": " + snippet
		}
		if snippet != "" {
			rows = append(rows, IndexRow{
				ID:        RowID(SourceMessage, msg.ID, "blueprint"),
				ProjectID: projectID,
				Source:    SourceMessage,
				HitKind:   HitKindMessage,
				SessionID: sessionID,
				MessageID: msg.ID,
				SourceRef: msg.ID,
				Kind:      "blueprint",
				Role:      role,
				Snippet:   truncateSnippet(snippet, 2000),
				TS:        ts,
			})
		}
	}

	if msg.WorkerSummary != nil {
		rows = append(rows, workerManifestRows(projectID, sessionID, msg, ts, role)...)
	}

	return rows
}

func workerManifestRows(projectID, sessionID string, msg api.Message, ts, role string) []IndexRow {
	if msg.WorkerSummary == nil {
		return nil
	}
	if msg.WorkerSummary.Grounding != nil {
		return nil
	}
	paths, findings := workerManifestEvidence(msg.Content)
	status := string(msg.WorkerSummary.Status)
	out := make([]IndexRow, 0, len(paths)+len(findings)+1)
	for i, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		out = append(out, IndexRow{
			ID:        RowID(SourceMessage, msg.ID, rowSuffix("worker_path", i, path)),
			ProjectID: projectID,
			Source:    SourceMessage,
			HitKind:   HitKindMessage,
			SessionID: sessionID,
			MessageID: msg.ID,
			SourceRef: msg.ID,
			Path:      path,
			Kind:      "worker_manifest",
			Role:      role,
			Snippet:   status + " · " + path,
			TS:        ts,
		})
	}
	for i, finding := range findings {
		path := strings.TrimSpace(finding.path)
		if path == "" {
			continue
		}
		snippet := status + " · " + path
		if ev := strings.TrimSpace(finding.evidence); ev != "" {
			snippet = snippet + " · " + truncateSnippet(ev, 200)
		}
		out = append(out, IndexRow{
			ID:        RowID(SourceMessage, msg.ID, rowSuffix("worker_finding", i, path)),
			ProjectID: projectID,
			Source:    SourceMessage,
			HitKind:   HitKindMessage,
			SessionID: sessionID,
			MessageID: msg.ID,
			SourceRef: msg.ID,
			Path:      path,
			Kind:      "worker_manifest",
			Role:      role,
			Snippet:   snippet,
			TS:        ts,
		})
	}
	if len(out) > 0 {
		return out
	}
	content := strings.TrimSpace(msg.Content)
	if content == "" {
		return nil
	}
	return []IndexRow{{
		ID:        RowID(SourceMessage, msg.ID, "worker_manifest"),
		ProjectID: projectID,
		Source:    SourceMessage,
		HitKind:   HitKindMessage,
		SessionID: sessionID,
		MessageID: msg.ID,
		SourceRef: msg.ID,
		Kind:      "worker_manifest",
		Role:      role,
		Snippet:   truncateSnippet(content, 2000),
		TS:        ts,
	}}
}

type workerManifestFinding struct {
	path     string
	evidence string
}

func workerManifestEvidence(content string) (paths []string, findings []workerManifestFinding) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, nil
	}
	seen := map[string]struct{}{}
	addPath := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}

	if strings.Contains(content, "<task") {
		if doc, ok := workercompletionxml.Unmarshal(content); ok {
			findings = appendWorkerProofJSON(doc.ProofJSON, doc.ReportJSON, addPath, findings)
			if len(paths) > 0 || len(findings) > 0 {
				return paths, findings
			}
		}
	}

	var proof struct {
		ChangedPaths []string `json:"changed_paths"`
	}
	if json.Unmarshal([]byte(content), &proof) == nil {
		for _, p := range proof.ChangedPaths {
			addPath(p)
		}
	}
	if len(paths) == 0 {
		var envelope struct {
			Proof struct {
				ChangedPaths []string `json:"changed_paths"`
			} `json:"proof"`
			Report struct {
				FilesModified []string `json:"files_modified"`
				Findings      []struct {
					Path     string `json:"path"`
					Evidence string `json:"evidence"`
				} `json:"findings"`
			} `json:"report"`
		}
		if json.Unmarshal([]byte(content), &envelope) == nil {
			for _, p := range envelope.Proof.ChangedPaths {
				addPath(p)
			}
			for _, p := range envelope.Report.FilesModified {
				addPath(p)
			}
			for _, f := range envelope.Report.Findings {
				findings = append(findings, workerManifestFinding{path: f.Path, evidence: f.Evidence})
			}
		}
	}
	return paths, findings
}

func appendWorkerProofJSON(
	proofJSON, reportJSON string,
	addPath func(string),
	findings []workerManifestFinding,
) []workerManifestFinding {
	if proofJSON = strings.TrimSpace(proofJSON); proofJSON != "" {
		var proof struct {
			ChangedPaths []string `json:"changed_paths"`
		}
		if json.Unmarshal([]byte(proofJSON), &proof) == nil {
			for _, p := range proof.ChangedPaths {
				addPath(p)
			}
		}
	}
	if reportJSON = strings.TrimSpace(reportJSON); reportJSON != "" {
		var report struct {
			FilesModified []string `json:"files_modified"`
			Findings      []struct {
				Path     string `json:"path"`
				Evidence string `json:"evidence"`
			} `json:"findings"`
		}
		if json.Unmarshal([]byte(reportJSON), &report) == nil {
			for _, p := range report.FilesModified {
				addPath(p)
			}
			for _, f := range report.Findings {
				findings = append(findings, workerManifestFinding{path: f.Path, evidence: f.Evidence})
			}
		}
	}
	return findings
}
