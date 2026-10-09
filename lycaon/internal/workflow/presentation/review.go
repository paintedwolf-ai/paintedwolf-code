package presentation

import (
	"context"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

// PhaseVerdict is one review phase's last decided record, with the schema it
// was recorded under.
type PhaseVerdict struct {
	Phase  string
	Label  string
	Def    workflowdef.ReviewLoopDef
	Record evidence.Record
}

// RunClaim is one claim as the run's review phases left it.
type RunClaim struct {
	ID string
	// Title is the one line the phase that introduced the claim gave it.
	Title string
	// Statement, Status, and CitedEvidence are the last phase's.
	Statement     string
	Status        string
	Class         workflowdef.ClaimClass
	CitedEvidence []api.CitationGroundingCitedEvidence
	// Origin is the phase that introduced the claim; Phase the last to state it.
	Origin string
	Phase  string
	// Dropped marks a claim the reconciling phase did not restate.
	Dropped bool
	// Answers are the rating answers the latest phase that gave any gave.
	Answers      map[string]string
	ScanGroupIDs []string
}

// VerdictMembers flattens a stamped record's string members. The reserved
// citation channels are not members, and a composite value is never a
// member's text.
func VerdictMembers(arts map[string]any) map[string]string {
	out := make(map[string]string, len(arts))
	for k, v := range arts {
		if k == evidence.CitedEvidenceArtifactKey || k == evidence.CitedURLsArtifactKey {
			continue
		}
		if s, ok := v.(string); ok && s != "" {
			out[k] = s
		}
	}
	return out
}

// ReviewEvidenceLister reads a run's review records for one phase slot.
type ReviewEvidenceLister interface {
	ListReviewLoopEvidence(ctx context.Context, sessionID, workflowRunID, phaseSlot string) ([]evidence.Record, error)
}

// ReviewVerdicts reads every review phase's last decided record, in manifest
// phase order. A later phase's verdict is a further decision, never a
// replacement for an earlier one.
func ReviewVerdicts(ctx context.Context, lister ReviewEvidenceLister, run *api.WorkflowRun, manifest workflowdef.Manifest) []PhaseVerdict {
	if lister == nil || run == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []PhaseVerdict
	for _, def := range manifest.PhaseDefs {
		if def.ReviewLoop == nil {
			continue
		}
		id := strings.TrimSpace(def.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		rec, ok := lastDecidedRecord(ctx, lister, run, id)
		if !ok {
			continue
		}
		out = append(out, PhaseVerdict{Phase: id, Label: def.ActivityLabel, Def: *def.ReviewLoop, Record: rec})
	}
	return out
}

// lastDecidedRecord is the newest record in a slot that carries a decision.
func lastDecidedRecord(ctx context.Context, lister ReviewEvidenceLister, run *api.WorkflowRun, slot string) (rec evidence.Record, ok bool) {
	recs, err := lister.ListReviewLoopEvidence(ctx, run.SessionID, run.ID, slot)
	if err != nil {
		return rec, false
	}
	for i := range recs {
		candidate := recs[i]
		if _, isString := candidate.Artifacts[workflowdef.VerdictDecisionKey].(string); !isString && strings.TrimSpace(candidate.Summary) == "" {
			continue
		}
		rec, ok = candidate, true
	}
	return rec, ok
}

// RunSetAsides are the set-asides the run's review phases recorded, in phase
// order: scanner groups a review accounted for without assessing one by one.
func RunSetAsides(verdicts []PhaseVerdict) []scanfindings.SetAside {
	var out []scanfindings.SetAside
	for _, v := range verdicts {
		setAsides, err := workflowvalidation.ParseVerdictSetAsides(v.Def, VerdictMembers(v.Record.Artifacts))
		if err != nil {
			continue
		}
		out = append(out, SetAsideSelectors(setAsides)...)
	}
	return out
}

// ReconcileClaims follows each claim through the phases in order. The phase
// that introduces a claim titles it; each later phase that restates it settles
// its status; a reconciling phase that leaves an earlier claim out leaves it
// open.
func ReconcileClaims(verdicts []PhaseVerdict) []RunClaim {
	var out []RunClaim
	index := map[string]int{}
	byPhase := map[string][]string{}
	for _, v := range verdicts {
		byField, err := workflowvalidation.ParseVerdictClaims(v.Def, VerdictMembers(v.Record.Artifacts))
		if err != nil {
			continue
		}
		present := map[string]bool{}
		for _, field := range workflowvalidation.SortedClaimFields(byField) {
			for _, c := range byField[field] {
				id := strings.TrimSpace(c.ID)
				present[id] = true
				byPhase[v.Phase] = append(byPhase[v.Phase], id)
				i, seen := index[id]
				if !seen {
					i = len(out)
					index[id] = i
					out = append(out, RunClaim{ID: id, Title: strings.TrimSpace(c.Title), Origin: v.Phase})
				}
				rc := &out[i]
				rc.Statement = strings.TrimSpace(c.Statement)
				rc.Status = strings.TrimSpace(c.Status)
				rc.Class = v.Def.ClassOf(c.Status)
				rc.CitedEvidence = append([]api.CitationGroundingCitedEvidence(nil), c.CitedEvidence...)
				rc.Phase = v.Phase
				rc.Dropped = false
				if len(c.Answers) > 0 {
					rc.Answers = cloneAnswers(c.Answers)
				}
				if len(c.ScanGroupIDs) > 0 {
					rc.ScanGroupIDs = append([]string(nil), c.ScanGroupIDs...)
				}
			}
		}
		for _, id := range byPhase[v.Def.ReconcilesPhase] {
			if !present[id] {
				rc := &out[index[id]]
				rc.Class = workflowdef.ClaimOpen
				rc.Dropped = true
			}
		}
	}
	return out
}

func cloneAnswers(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

// VerdictRulesFor collects what a verdict in the run's current phase is also
// checked against: the claim ids earlier records introduced, and the rating
// questions the manifest declares.

func SetAsideSelectors(in []guidance.CoordinatorSetAside) []scanfindings.SetAside {
	out := make([]scanfindings.SetAside, 0, len(in))
	for _, sa := range in {
		out = append(out, scanfindings.SetAside{GroupIDs: sa.ScanGroupIDs, Scanner: sa.Scanner, Paths: sa.Paths, Reason: sa.Reason})
	}
	return out
}
