package editordoc

import (
	"context"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/observability/cmddiag"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	weaMaxEditDiagItems    = 64
	weaMaxRetainedVersions = 8
)

type editDiagRef struct {
	projectID   string
	branchID    sourcebranch.ID
	fileID      string
	operationID string
	heldVerID   string
	path        string
}

// observeAcceptedAgentEditsWEA emits a bounded WEA edit observation after
// ApplyAgentEdits commits. It reads actual owning read-basis records from
// a.agentReads and, when sourceledger.History is configured, performs a bounded
// deferred read (via cmddiag.ObserveTask) of the retained sourceledger.Version
// attribution on the async worker goroutine. Where an independent owner witness
// is unavailable, it marks the observation incomplete instead of fabricating
// matching facts. Zero overhead when diagnostics are disabled.
func (a *AgentService) observeAcceptedAgentEditsWEA(batch []*agentAcceptance, results []*AgentEditResult) bool {
	if !cmddiag.Enabled() || a == nil || len(batch) == 0 {
		return false
	}
	limit := min(len(batch), weaMaxEditDiagItems)
	readBases := make([]cmddiag.WEAReadBasisFact, 0, limit)
	editEffects := make([]cmddiag.WEAEditEffectFact, 0, limit)
	refs := make([]editDiagRef, 0, limit)
	missingReadWitness := false

	var sessionID, projectID, invocationID string
	for i := 0; i < limit; i++ {
		item := batch[i]
		if item == nil || item.document == nil {
			continue
		}
		in := item.input
		d := item.document
		sess := strings.TrimSpace(in.SessionID)
		if sessionID == "" {
			sessionID = sess
		}
		if projectID == "" {
			projectID = strings.TrimSpace(in.ProjectID)
		}
		inv := strings.TrimSpace(in.ToolCallID)
		if inv == "" {
			inv = strings.TrimSpace(in.OperationID)
		}
		if invocationID == "" {
			invocationID = inv
		}

		basisRev := strconv.FormatInt(in.ExpectedRevision, 10)
		prevRev := strconv.FormatInt(item.previous, 10)
		rebased := in.ExpectedRevision != item.previous

		// Read the actual owning agent read-basis record from a.agentReads.
		if basis, ok := a.lookupOwnedReadBasis(in.ProjectID, sess, d.ID); ok {
			readBases = append(readBases, cmddiag.WEAReadBasisFact{
				ReadID:       "read:" + d.ID + ":" + strconv.FormatInt(basis.revision, 10),
				SessionID:    sess,
				InvocationID: inv,
				Path:         d.Path,
				Revision:     strconv.FormatInt(basis.revision, 10),
				Qualifying:   basis.whole,
				Active:       true,
				SourceRef:    cmddiag.DigestReference("agent_read_cache", d.ID),
			})
		} else {
			missingReadWitness = true
		}

		heldVerID := ""
		if i < len(results) && results[i] != nil {
			heldVerID = strings.TrimSpace(results[i].HeldVersionID)
		}
		authorID := strings.TrimSpace(d.authorship.toolCallID)
		if authorID == "" {
			authorID = strings.TrimSpace(d.authorship.sessionID)
		}
		authorKind := ""
		if item.mutation != nil {
			authorKind = string(api.SourceChangeOriginAgent)
		}

		opID := strings.TrimSpace(in.OperationID)
		editEffects = append(editEffects, cmddiag.WEAEditEffectFact{
			EffectID:            opID,
			VersionID:           heldVerID,
			SessionID:           sess,
			InvocationID:        inv,
			Path:                d.Path,
			Accepted:            true,
			ReadBasisRevision:   basisRev,
			CurrentRevision:     prevRev,
			RebasedCleanly:      rebased,
			RebasedFromRevision: basisRev,
			AuthorID:            authorID,
			AuthorKind:          authorKind,
			SourceRef:           cmddiag.DigestReference("edit_effect", opID),
		})
		refs = append(refs, editDiagRef{
			projectID:   strings.TrimSpace(d.ProjectID),
			branchID:    d.BranchID,
			fileID:      strings.TrimSpace(d.FileID),
			operationID: opID,
			heldVerID:   heldVerID,
			path:        d.Path,
		})
	}

	if len(editEffects) == 0 {
		return false
	}

	baseObs := cmddiag.WEAObservation{
		SessionID:              sessionID,
		ProjectID:              projectID,
		InvocationID:           invocationID,
		ScopeKey:               editEffects[0].EffectID,
		Settled:                true,
		Truncated:              len(batch) > limit,
		ReadWitnessUnavailable: missingReadWitness,
		ReadBases:              readBases,
		EditEffects:            editEffects,
	}

	var history *sourceledger.History
	if a.s != nil {
		history = a.s.history
	}
	if history == nil {
		baseObs.VersionWitnessUnavailable = true
		return cmddiag.ObserveWEA(baseObs)
	}

	// Perform a bounded deferred read of retained version attribution on the
	// async diagnostics worker so zero DB queries run on the caller goroutine.
	return cmddiag.ObserveTask(cmddiag.Task{
		SessionID:  sessionID,
		ProgramID:  baseObs.ScopeKey,
		ToolCallID: invocationID,
		Fetch: func(ctx context.Context) (cmddiag.Observation, error) {
			obs := baseObs
			var versions []cmddiag.WEAFileVersionFact
			for _, ref := range refs {
				if ref.projectID == "" || ref.fileID == "" {
					continue
				}
				page, err := history.QueryFileVersions(ctx, ref.projectID, ref.fileID, weaMaxRetainedVersions, 0)
				if err != nil {
					continue
				}
				var matched *sourceledger.Version
				for i := range page.Versions {
					ver := &page.Versions[i]
					if (ref.heldVerID != "" && ver.ID == ref.heldVerID) ||
						(ref.operationID != "" && (ver.OperationID == ref.operationID || ver.EffectID == ref.operationID)) {
						matched = ver
						break
					}
					if matched == nil && (ref.branchID == "" || ver.BranchID == ref.branchID) {
						matched = ver
					}
				}
				if matched != nil {
					verAuthor := strings.TrimSpace(matched.ToolCallID)
					if verAuthor == "" {
						verAuthor = strings.TrimSpace(matched.SessionID)
					}
					effectID := ref.operationID
					if effectID == "" {
						effectID = strings.TrimSpace(matched.OperationID)
					}
					versions = append(versions, cmddiag.WEAFileVersionFact{
						VersionID:  matched.ID,
						EffectID:   effectID,
						SessionID:  matched.SessionID,
						WorkerID:   matched.JobID,
						Path:       matched.Path,
						AuthorID:   verAuthor,
						AuthorKind: string(matched.Origin),
						SourceRef:  cmddiag.DigestReference("sourceledger_version", matched.ID),
					})
				}
			}
			obs.FileVersions = versions
			if len(versions) == 0 {
				obs.VersionWitnessUnavailable = true
			}
			return cmddiag.WrapWEAObservation(obs), nil
		},
	})
}

func (a *AgentService) lookupOwnedReadBasis(projectID, sessionID, documentID string) (agentReadBasis, bool) {
	if a == nil || sessionID == "" || documentID == "" {
		return agentReadBasis{}, false
	}
	a.agentReads.mu.Lock()
	defer a.agentReads.mu.Unlock()
	if a.agentReads.bases == nil {
		return agentReadBasis{}, false
	}
	basis, ok := a.agentReads.bases[agentReadKey{project: projectID, session: sessionID, document: documentID}]
	return basis, ok
}
