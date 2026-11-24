package inject

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// RecordedVerdictField is one member of a verdict the run has already stamped.
type RecordedVerdictField struct {
	Name  string
	Value string
}

// RecordedVerdict is one terminal review_loop verdict the run has stamped.
// Workers have no tool that resolves an evidence_key, so a brief naming one only
// means something when the record travels with it.
type RecordedVerdict struct {
	Phase       string
	EvidenceKey string
	Fields      []RecordedVerdictField
}

// WorkerTaskAssignmentInput is the host-defined charter for a worker leg.
type WorkerTaskAssignmentInput struct {
	CoverageAssignment *reviewcoverage.Binding
	ScanInventory      string
	SessionID          string
	ProjectDir         string
	Charter            api.WorkerTaskCharter
	WorkerName         string
	RoundInstructions  string
	ViewKind           string
	HasGit             bool
	AgentType          string
	WorkerJobID        string
	Scope              api.TaskScope
	Attachments        []promptattach.ForwardedAttachment
	// RecordedVerdicts are the run's stamped verdicts in phase order.
	RecordedVerdicts []RecordedVerdict
}

// WorkerTaskAssignmentData is the pongo data model for inject/worker-task-assignment.md.
type WorkerTaskAssignmentData struct {
	CoverageRequired   bool
	CoverageAssignment string
	ScanInventory      string
	WorkerName         string
	RoundInstructions  string
	ViewKind           string
	HasGit             bool
	AgentType          string
	WorkerJobID        string
	SharedContext      string
	Goal               string
	KnownFacts         []string
	Constraints        []string
	DoneWhen           []string
	ContextRefs        []string
	ScopeMode          string
	ScopePaths         []string
	LinkedCheckout     bool
	FileOrientations   []ScopeFileOrientation
	Attachments        []promptattach.ForwardedAttachment
	RecordedVerdicts   []RecordedVerdict
	// Elisions names every list this assignment shortened.
	Elisions []string
}

// BuildWorkerTaskAssignmentData maps runtime facts into inject DTO fields.
func BuildWorkerTaskAssignmentData(ctx context.Context, in WorkerTaskAssignmentInput) WorkerTaskAssignmentData {
	norm := in.Scope.Normalized()
	var elisions []string
	charter := func(what string, items []string) []string {
		kept, elided := bound(trimWorkerCharterItems(items), MaxCharterItems)
		if elided > 0 {
			elisions = append(elisions, elisionNote(what, len(kept), elided))
		}
		return kept
	}
	paths, pathsElided := bound(append([]string(nil), norm.Paths...), MaxScopePaths)
	if pathsElided > 0 {
		elisions = append(elisions, elisionNote("suggested paths", len(paths), pathsElided))
	}
	attachments, attachElided := bound(append([]promptattach.ForwardedAttachment(nil), in.Attachments...), MaxForwardedAttachments)
	if attachElided > 0 {
		elisions = append(elisions, elisionNote("forwarded attachments", len(attachments), attachElided))
	}
	verdicts, verdictsElided := bound(append([]RecordedVerdict(nil), in.RecordedVerdicts...), MaxRecordedVerdicts)
	if verdictsElided > 0 {
		elisions = append(elisions, elisionNote("recorded verdicts", len(verdicts), verdictsElided))
	}
	for i, v := range verdicts {
		fields, fieldsElided := bound(v.Fields, MaxRecordedVerdictFields)
		if fieldsElided > 0 {
			elisions = append(elisions, elisionNote("fields of verdict `"+v.EvidenceKey+"`", len(fields), fieldsElided))
		}
		verdicts[i].Fields = fields
	}
	viewKind := strings.TrimSpace(in.ViewKind)
	if viewKind == "" && norm.Mode == api.TaskScopeModeWrite {
		viewKind = "materialized"
	}
	_, discoveredGit := gitrepo.Discover(in.ProjectDir)
	hasGit := norm.Mode != api.TaskScopeModeWrite && (in.HasGit || discoveredGit)
	data := WorkerTaskAssignmentData{
		WorkerName:        strings.TrimSpace(in.WorkerName),
		RoundInstructions: strings.TrimSpace(in.RoundInstructions),
		ViewKind:          viewKind,
		HasGit:            hasGit,
		AgentType:         strings.TrimSpace(in.AgentType),
		WorkerJobID:       strings.TrimSpace(in.WorkerJobID),
		Goal:              strings.TrimSpace(in.Charter.Goal),
		SharedContext:     in.Charter.SharedContext,
		KnownFacts:        charter("known facts", in.Charter.KnownFacts),
		Constraints:       charter("constraints", in.Charter.Constraints),
		DoneWhen:          charter("done-when criteria", in.Charter.DoneWhen),
		ContextRefs:       charter("context refs", in.Charter.ContextRefs),
		ScopeMode:         string(norm.Mode),
		ScopePaths:        paths,
		LinkedCheckout:    norm.Mode == api.TaskScopeModeWrite && gitrepo.ScopeIncludesLinkedCheckout(in.ProjectDir, norm.Paths),
		Attachments:       attachments,

		RecordedVerdicts: verdicts,
		ScanInventory:    in.ScanInventory,
	}
	if orientations, err := BuildScopeFileOrientations(ctx, in.ProjectDir, in.Scope); err == nil {
		data.FileOrientations = orientations
		for _, o := range orientations {
			if o.SymbolsElided > 0 {
				elisions = append(elisions, elisionNote("outline symbols for `"+o.Path+"`", len(o.Symbols), o.SymbolsElided))
			}
		}
	}
	if in.CoverageAssignment != nil {
		data.CoverageRequired = in.CoverageAssignment.CoverageRequired
		if raw, err := surveyjson.Marshal(reviewcoverage.FirstPage(*in.CoverageAssignment)); err == nil {
			data.CoverageAssignment = string(raw)
		}
	}
	data.Elisions = elisions
	return data
}

func trimWorkerCharterItems(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// WorkerTaskAssignmentToMap converts the DTO for pongo2 render.
func WorkerTaskAssignmentToMap(data WorkerTaskAssignmentData) map[string]any {
	orientations := make([]map[string]any, 0, len(data.FileOrientations))
	for _, o := range data.FileOrientations {
		symbols := make([]map[string]any, 0, len(o.Symbols))
		for _, sym := range o.Symbols {
			symbols = append(symbols, map[string]any{
				"kind": sym.Kind,
				"name": sym.Name,
				"line": sym.Line,
			})
		}
		orientations = append(orientations, map[string]any{
			"path":           o.Path,
			"total_lines":    o.TotalLines,
			"size_bytes":     o.SizeBytes,
			"source":         o.Source,
			"symbols":        symbols,
			"symbols_elided": o.SymbolsElided,
		})
	}
	attachments := make([]map[string]any, 0, len(data.Attachments))
	for _, a := range data.Attachments {
		if strings.TrimSpace(a.Path) == "" {
			continue
		}
		item := map[string]any{
			"filename": a.Filename,
			"mime":     a.MIME,
			"path":     a.Path,
			"kind":     string(a.Kind),
			"hint":     a.Hint(),
		}
		if a.SizeBytes > 0 {
			item["size_bytes"] = a.SizeBytes
		}
		attachments = append(attachments, item)
	}
	recorded := make([]map[string]any, 0, len(data.RecordedVerdicts))
	for _, v := range data.RecordedVerdicts {
		fields := make([]map[string]any, 0, len(v.Fields))
		for _, f := range v.Fields {
			fields = append(fields, map[string]any{"name": f.Name, "value": f.Value})
		}
		recorded = append(recorded, map[string]any{
			"phase":        v.Phase,
			"evidence_key": v.EvidenceKey,
			"fields":       fields,
		})
	}
	return map[string]any{
		"scan_inventory":      data.ScanInventory,
		"coverage_assignment": data.CoverageAssignment,
		"coverage_required":   data.CoverageRequired,
		"worker_name":         data.WorkerName,
		"round_instructions":  data.RoundInstructions,
		"view_kind":           data.ViewKind,
		"has_git":             data.HasGit,
		"agent_type":          data.AgentType,
		"worker_job_id":       data.WorkerJobID,
		"goal":                data.Goal,
		"shared_context":      data.SharedContext,
		"known_facts":         data.KnownFacts,
		"constraints":         data.Constraints,
		"done_when":           data.DoneWhen,
		"context_refs":        data.ContextRefs,
		"scope_mode":          data.ScopeMode,
		"scope_paths":         data.ScopePaths,
		"linked_checkout":     data.LinkedCheckout,
		"file_orientations":   orientations,
		"attachments":         attachments,
		"recorded_verdicts":   recorded,
		"elisions":            data.Elisions,
	}
}

// RenderWorkerTaskAssignment renders the worker assignment inject.
func RenderWorkerTaskAssignment(ctx context.Context, renderer *prompts.InjectRenderer, in WorkerTaskAssignmentInput) (string, error) {
	if renderer == nil {
		return "", fmt.Errorf("inject renderer not configured")
	}
	data := BuildWorkerTaskAssignmentData(ctx, in)
	block, err := anchor.RenderInform(ctx, anchor.InjectWorkerTaskAssignment, anchor.MatchContext{Surface: "worker", SessionID: in.SessionID}, renderer, WorkerTaskAssignmentToMap(data))
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" {
		return "", fmt.Errorf("worker-task-assignment inject rendered empty")
	}
	if !strings.Contains(block, guidance.MarkerWorkerTaskAssignment) {
		return "", fmt.Errorf("worker-task-assignment inject missing sentinel %q", guidance.MarkerWorkerTaskAssignment)
	}
	return block, nil
}
