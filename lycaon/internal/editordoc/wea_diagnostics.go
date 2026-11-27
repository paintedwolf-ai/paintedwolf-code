package editordoc

import (
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/observability/cmddiag"
)

const weaMaxEditDiagItems = 64

// observeAcceptedAgentEditsWEA emits a bounded WEA edit read-basis and author
// attribution observation after ApplyAgentEdits commits. Legitimate CRDT
// rebases (ExpectedRevision != previous after planAgentEdit succeeded) are
// marked RebasedCleanly so clean rebases never emit false-positive warnings.
// When diagnostics are disabled, this returns immediately with zero allocations.
func observeAcceptedAgentEditsWEA(batch []*agentAcceptance, results []*AgentEditResult) bool {
	if !cmddiag.Enabled() || len(batch) == 0 {
		return false
	}
	limit := min(len(batch), weaMaxEditDiagItems)
	readBases := make([]cmddiag.WEAReadBasisFact, 0, limit)
	editEffects := make([]cmddiag.WEAEditEffectFact, 0, limit)
	fileVersions := make([]cmddiag.WEAFileVersionFact, 0, limit)

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
		resultRev := strconv.FormatInt(d.Revision, 10)
		rebased := in.ExpectedRevision != item.previous

		versionID := ""
		if i < len(results) && results[i] != nil && strings.TrimSpace(results[i].HeldVersionID) != "" {
			versionID = strings.TrimSpace(results[i].HeldVersionID)
		} else {
			versionID = d.ID + ":rev:" + resultRev
		}

		authorID := strings.TrimSpace(in.ToolCallID)
		if authorID == "" {
			authorID = sess
		}
		if authorID == "" {
			authorID = "agent"
		}

		readBases = append(readBases, cmddiag.WEAReadBasisFact{
			ReadID:       "pin:" + d.ID + ":" + basisRev,
			SessionID:    sess,
			InvocationID: inv,
			Path:         d.Path,
			Revision:     basisRev,
			Qualifying:   true,
			Active:       true,
			SourceRef:    cmddiag.DigestReference("read_basis", d.ID, basisRev),
		})
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
			AuthorKind:          "agent",
			SourceRef:           cmddiag.DigestReference("edit_effect", in.OperationID),
		})
		fileVersions = append(fileVersions, cmddiag.WEAFileVersionFact{
			VersionID:      versionID,
			EffectID:       strings.TrimSpace(in.OperationID),
			SessionID:      sess,
			Path:           d.Path,
			AuthorID:       authorID,
			AuthorKind:     "agent",
			BaseRevision:   prevRev,
			ResultRevision: resultRev,
			SourceRef:      cmddiag.DigestReference("file_version", versionID),
		})
	}

	if len(editEffects) == 0 {
		return false
	}
	return cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID:    sessionID,
		ProjectID:    projectID,
		InvocationID: invocationID,
		ScopeKey:     editEffects[0].EffectID,
		Settled:      true,
		ReadBases:    readBases,
		EditEffects:  editEffects,
		FileVersions: fileVersions,
	})
}
