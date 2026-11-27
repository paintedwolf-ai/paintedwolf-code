package editordoc

import (
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/observability/cmddiag"
)

const weaMaxEditDiagItems = 64

// observeAcceptedAgentEditsWEA emits a bounded WEA edit observation after
// ApplyAgentEdits commits. It reads actual owning read-basis records from
// a.agentReads without fabricating qualifying reads or companion version rows.
// Where an independent owner witness (read record or persisted version row) is
// unavailable at this seam, it marks the observation incomplete instead of
// self-confirming. Zero overhead when diagnostics are disabled.
func (a *AgentService) observeAcceptedAgentEditsWEA(batch []*agentAcceptance, results []*AgentEditResult) bool {
	if !cmddiag.Enabled() || a == nil || len(batch) == 0 {
		return false
	}
	limit := min(len(batch), weaMaxEditDiagItems)
	readBases := make([]cmddiag.WEAReadBasisFact, 0, limit)
	editEffects := make([]cmddiag.WEAEditEffectFact, 0, limit)
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

		versionID := ""
		if i < len(results) && results[i] != nil {
			versionID = strings.TrimSpace(results[i].HeldVersionID)
		}
		// Record only persisted effect authorship; never hardcode author kind
		// or fabricate a companion FileVersions row from the same input.
		authorID := strings.TrimSpace(d.authorship.toolCallID)
		if authorID == "" {
			authorID = strings.TrimSpace(d.authorship.sessionID)
		}

		editEffects = append(editEffects, cmddiag.WEAEditEffectFact{
			EffectID:            strings.TrimSpace(in.OperationID),
			VersionID:           versionID,
			SessionID:           sess,
			InvocationID:        inv,
			Path:                d.Path,
			Accepted:            true,
			ReadBasisRevision:   basisRev,
			CurrentRevision:     prevRev,
			RebasedCleanly:      rebased,
			RebasedFromRevision: basisRev,
			AuthorID:            authorID,
			SourceRef:           cmddiag.DigestReference("edit_effect", in.OperationID),
		})
	}

	if len(editEffects) == 0 {
		return false
	}
	return cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID:                 sessionID,
		ProjectID:                 projectID,
		InvocationID:              invocationID,
		ScopeKey:                  editEffects[0].EffectID,
		Settled:                   true,
		Truncated:                 len(batch) > limit,
		ReadWitnessUnavailable:    missingReadWitness,
		VersionWitnessUnavailable: true, // Persisted version row is owned by sourceledger, not fabricated here.
		ReadBases:                 readBases,
		EditEffects:               editEffects,
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
