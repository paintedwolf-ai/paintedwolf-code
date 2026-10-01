package workercompletion

import (
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
	"github.com/lycaon/lycaon/internal/verification"
	"github.com/lycaon/lycaon/pkg/api"
)

// maxSummaryExcerptBytes bounds the summary excerpt in a completion digest.
const maxSummaryExcerptBytes = 600

// WorkerCompletionProof is structured host proof compiled from a worker child session.
type WorkerCompletionProof struct {
	ChangedPaths   []string `json:"changed_paths,omitempty"`
	WorkspaceDirty bool     `json:"workspace_dirty,omitempty"`
	MutationTools  []string `json:"mutation_tools,omitempty"`
	SurveyTools    []string `json:"survey_tools,omitempty"`
	ReceiptCount   int      `json:"receipt_count,omitempty"`
	// VisualArtifactIDs excludes live recordings.
	VisualArtifactIDs []string `json:"visual_artifact_ids,omitempty"`
	// InvocationReceipts retain observed validation outcomes.
	InvocationReceipts []WorkerInvocationReceipt `json:"invocation_receipts,omitempty"`
	// SourceRevision identifies the source content whose changed paths are reported.
	SourceRevision   string                   `json:"source_revision,omitempty"`
	SourceRootDigest string                   `json:"source_root_digest,omitempty"`
	DeclaredCommand  *string                  `json:"declared_command,omitempty"`
	Verification     *verification.Assessment `json:"verification,omitempty"`
}

// SourceRevision identifies host-observed source content within a workspace root.
type SourceRevision struct {
	Revision   string
	RootDigest string
}

// WorkerInvocationReceipt is a host-observed terminal receipt.
type WorkerInvocationReceipt struct {
	CheckID          string               `json:"check_id,omitempty"`
	IsCheck          bool                 `json:"is_check,omitempty"`
	Command          string               `json:"command,omitempty"`
	Cwd              string               `json:"cwd,omitempty"`
	ID               string               `json:"id"`
	Tool             string               `json:"tool"`
	Status           api.InvocationStatus `json:"status"`
	EvidenceKind     string               `json:"evidence_kind"`
	EvidenceRef      string               `json:"evidence_ref,omitempty"`
	Verdict          string               `json:"verdict,omitempty"`
	SourceRevision   string               `json:"source_revision,omitempty"`
	SourceRootDigest string               `json:"source_root_digest,omitempty"`
}

// maxSuggestedNextBytes bounds the suggested-next line in a completion digest.
const maxSuggestedNextBytes = 240

// BuildWorkerCompletionProof compiles host proof from child history.
func BuildWorkerCompletionProof(
	childMessages []api.Message,
	changedPaths []string,
	currentRevision SourceRevision,
) WorkerCompletionProof {
	proof := WorkerCompletionProof{
		MutationTools:      collectMutationTools(childMessages),
		SurveyTools:        collectSurveyTools(childMessages),
		ReceiptCount:       countSurveyReceipts(childMessages),
		VisualArtifactIDs:  CollectPresentableVisualArtifactIDs(childMessages),
		InvocationReceipts: collectInvocationReceipts(childMessages),
	}
	proof.SourceRevision = strings.TrimSpace(currentRevision.Revision)
	proof.SourceRootDigest = strings.TrimSpace(currentRevision.RootDigest)
	if report, _, ok := LastCompleteLegReport(childMessages); ok {
		proof.Verification = report.Verification
	}
	if len(changedPaths) > 0 {
		proof.ChangedPaths = changedPaths
		proof.WorkspaceDirty = true
	}
	return proof
}

func collectInvocationReceipts(msgs []api.Message) []WorkerInvocationReceipt {
	seen := map[string]struct{}{}
	var out []WorkerInvocationReceipt
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleTool || msg.ToolResult == nil || msg.ToolResult.Invocation == nil {
			continue
		}
		r := msg.ToolResult.Invocation
		if strings.TrimSpace(r.ID) == "" {
			continue
		}
		if _, ok := seen[r.ID]; ok {
			continue
		}
		seen[r.ID] = struct{}{}
		out = append(out, WorkerInvocationReceipt{
			CheckID: r.ToolCallID,
			IsCheck: receiptIsCheck(r.Tool, msg.ToolResult.ToolArgs),
			Command: commandsurface.PrimaryCommandLine(msg.ToolResult.ToolArgs, nil),
			Cwd:     receiptCwd(msg.ToolResult.ToolArgs),
			ID:      r.ID, Tool: r.Tool, Status: r.Status,
			EvidenceKind: r.Evidence.Kind, EvidenceRef: r.Evidence.Ref,
			// Receipts retain verdicts independently of rendered result bodies.
			Verdict:        r.SourceVerdict,
			SourceRevision: r.SourceRevision, SourceRootDigest: r.SourceRootDigest,
		})
	}
	return out
}

// WithSourceRuns projects a process's terminal result over its launch-only receipt.
func (p WorkerCompletionProof) WithSourceRuns(runs []WorkerInvocationReceipt) WorkerCompletionProof {
	p.InvocationReceipts = append([]WorkerInvocationReceipt(nil), p.InvocationReceipts...)
	indices := make(map[string]int)
	for i, receipt := range p.InvocationReceipts {
		if receipt.CheckID != "" {
			indices[receipt.CheckID] = i
		}
	}
	for _, run := range runs {
		if run.CheckID == "" || run.Verdict == "" {
			continue
		}
		if i, ok := indices[run.CheckID]; ok {
			p.InvocationReceipts[i] = run
		} else {
			indices[run.CheckID] = len(p.InvocationReceipts)
			p.InvocationReceipts = append(p.InvocationReceipts, run)
		}
	}
	return p
}

func receiptIsCheck(tool string, args map[string]any) bool {
	isCheck, _ := args["verification"].(bool)
	return tool == "verify" || isCheck
}

func receiptCwd(args map[string]any) string {
	cwd, _ := args["cwd"].(string)
	if strings.TrimSpace(cwd) == "" {
		return "."
	}
	return strings.TrimSpace(cwd)
}

// CollectPresentableVisualArtifactIDs returns unique presentable stills.
func CollectPresentableVisualArtifactIDs(msgs []api.Message) []string {
	var ids []string
	seen := map[string]struct{}{}
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleTool || toolMessageFailed(msg) {
			continue
		}
		if msg.ToolResult == nil || msg.ToolResult.Visual == nil {
			continue
		}
		v := msg.ToolResult.Visual
		id := strings.TrimSpace(v.ID)
		if id == "" || !api.PresentableStillVisualMime(v.Mime) {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func collectMutationTools(msgs []api.Message) []string {
	seen := map[string]struct{}{}
	pending := map[string]string{}
	for _, msg := range msgs {
		switch msg.Role {
		case api.MessageRoleAssistant:
			pending = map[string]string{}
			for _, tc := range msg.ToolCalls {
				id := strings.TrimSpace(tc.ID)
				if id == "" {
					continue
				}
				pending[id] = strings.TrimSpace(tc.Name)
			}
		case api.MessageRoleTool:
			if toolMessageFailed(msg) {
				pending = map[string]string{}
				continue
			}
			for _, name := range pending {
				if toolcontract.MutatesContent(name) || name == "restore_version" {
					seen[name] = struct{}{}
				}
			}
			pending = map[string]string{}
		case api.MessageRoleUser, api.MessageRoleSystem:
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	return out
}

func collectSurveyTools(msgs []api.Message) []string {
	seen := map[string]struct{}{}
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleTool || toolMessageFailed(msg) {
			continue
		}
		if r, ok := surveyreceipt.Parse(msg.Content); ok {
			if name := strings.TrimSpace(r.Tool); name != "" {
				seen[name] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	return out
}

func countSurveyReceipts(msgs []api.Message) int {
	n := 0
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleTool || toolMessageFailed(msg) {
			continue
		}
		if _, ok := surveyreceipt.Parse(msg.Content); ok {
			n++
		}
	}
	return n
}

func FormatWorkerDigest(agentType, jobID, status, summary string, proof WorkerCompletionProof, report WorkerCompletionReport) string {
	var b strings.Builder
	agent := strings.TrimSpace(agentType)
	if agent == "" {
		agent = "worker"
	}
	fmt.Fprintf(&b, "Host worker digest — agent=%s status=%s", agent, strings.TrimSpace(status))
	if id := strings.TrimSpace(jobID); id != "" {
		fmt.Fprintf(&b, " job=%s", id)
	}
	b.WriteString("\n")
	report.Normalize()
	if report.LegStatus != "" {
		fmt.Fprintf(&b, "- Leg status: %s\n", report.LegStatus)
	}
	if len(report.FilesModified) > 0 {
		paths := report.FilesModified
		if len(paths) > 12 {
			paths = paths[:12]
		}
		fmt.Fprintf(&b, "- Files modified: %s\n", strings.Join(paths, ", "))
	}
	if len(report.ObjectivesMet) > 0 {
		items := report.ObjectivesMet
		if len(items) > 6 {
			items = items[:6]
		}
		fmt.Fprintf(&b, "- Objectives met: %s\n", strings.Join(items, "; "))
	}
	if len(report.RemainingRisk) > 0 {
		items := report.RemainingRisk
		if len(items) > 4 {
			items = items[:4]
		}
		fmt.Fprintf(&b, "- Remaining risk: %s\n", strings.Join(items, "; "))
	}
	if next := strings.TrimSpace(report.SuggestedNextTask); next != "" {
		next = runeclamp.ClampBytes(next, maxSuggestedNextBytes)
		fmt.Fprintf(&b, "- Suggested next: %s\n", next)
	}
	if proof.WorkspaceDirty {
		b.WriteString("- Workspace: dirty")
		if len(proof.ChangedPaths) > 0 {
			fmt.Fprintf(&b, " (%d paths)", len(proof.ChangedPaths))
		}
		b.WriteByte('\n')
	}
	if len(proof.MutationTools) > 0 {
		fmt.Fprintf(&b, "- Mutation tools: %s\n", strings.Join(proof.MutationTools, ", "))
	}
	if proof.ReceiptCount > 0 || len(proof.SurveyTools) > 0 {
		fmt.Fprintf(&b, "- Survey receipts: %d", proof.ReceiptCount)
		if len(proof.SurveyTools) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(proof.SurveyTools, ", "))
		}
		b.WriteByte('\n')
	}
	if len(proof.VisualArtifactIDs) > 0 {
		ids := proof.VisualArtifactIDs
		if len(ids) > 6 {
			ids = ids[len(ids)-6:]
		}
		fmt.Fprintf(&b, "- Visual artifacts: %s\n", strings.Join(ids, ", "))
	}
	if len(report.EvidenceObligations) > 0 {
		for _, obligation := range report.EvidenceObligations {
			fmt.Fprintf(&b, "- Evidence obligation: %s=%s", obligation.Kind, obligation.Status)
			if len(obligation.EvidenceRefs) > 0 {
				fmt.Fprintf(&b, " (%s)", strings.Join(obligation.EvidenceRefs, ", "))
			}
			if obligation.Reason != "" {
				fmt.Fprintf(&b, " — %s", obligation.Reason)
			}
			b.WriteByte('\n')
		}
	}
	summary = strings.TrimSpace(summary)
	if summary != "" {
		summary = runeclamp.ClampBytes(summary, maxSummaryExcerptBytes)
		fmt.Fprintf(&b, "- Summary excerpt: %s\n", summary)
	}
	return strings.TrimSpace(b.String())
}

func AppendWorkerDecisionDigest(digest string, req api.WorkerDecisionRequest) string {
	raw, err := json.Marshal(req)
	if err != nil {
		return digest
	}
	block := "last_worker_decision_request: " + string(raw)
	if strings.TrimSpace(digest) == "" {
		return block
	}
	return digest + "\n" + block
}
