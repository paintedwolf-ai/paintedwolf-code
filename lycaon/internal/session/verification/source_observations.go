package verification

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/pkg/api"
)

type SourceMatch string

const (
	SourceCurrent SourceMatch = "current"
	SourceChanged SourceMatch = "different_source"
	SourceUnknown SourceMatch = "unknown"
)

// SourceCheckObservation describes recorded evidence, not task completion.
type SourceCheckObservation struct {
	CheckID                 string                              `json:"-"`
	Command                 string                              `json:"command"`
	Cwd                     string                              `json:"cwd"`
	Verdict                 string                              `json:"recorded_verdict"`
	Match                   SourceMatch                         `json:"source_match"`
	PriorIntent             bool                                `json:"prior_user_intent"`
	StaleReason             string                              `json:"stale_reason,omitempty"`
	InvalidatingPaths       []layeredfs.QualifiedPath           `json:"invalidating_paths,omitempty"`
	ObservationRefs         []string                            `json:"observation_refs,omitempty"`
	UncertainOverflow       bool                                `json:"uncertain_overflow,omitempty"`
	AttributionCompleteness observation.AttributionCompleteness `json:"attribution_completeness,omitempty"`
}

type SourceCheckObservations struct {
	Checks                []SourceCheckObservation           `json:"checks"`
	Omitted               int                                `json:"older_checks_omitted"`
	RequiredByWorkflow    bool                               `json:"required_by_workflow"`
	RecordsUnavailable    bool                               `json:"records_unavailable,omitempty"`
	TrunkChanged          *observation.TrunkChangedNotice    `json:"trunk_changed,omitempty"`
	PromotionPreviewHints []observation.StaleObservationHint `json:"promotion_preview_hints,omitempty"`
}

// SourceObservations compares all displayed checks with one exact source observation.
// Historical verdicts remain unchanged, independently of current workflow requirements.
func (m *Service) SourceObservations(ctx context.Context, sess *api.Session, history []api.Message) *SourceCheckObservations {
	if m == nil || sess == nil || m.evidenceStore == nil {
		return nil
	}
	records, err := m.ReadSourceRecords(ctx, sess)
	if err != nil {
		return &SourceCheckObservations{RecordsUnavailable: true, RequiredByWorkflow: m.RequiresPhaseVerify(ctx, sess.ID)}
	}
	revision, root := m.Revision(ctx, m.SourceEvidenceRoot(ctx, sess))
	out := sourceCheckObservations(records, boundaryTime(history), revision, root)
	if len(out.Checks) == 0 {
		return nil
	}
	for i := range out.Checks {
		applyObservedFreshness(m, ctx, sess, &out.Checks[i])
	}
	if notice, ok := m.PendingTrunkChangedNotice(ctx, sess); ok {
		out.TrunkChanged = notice
	}
	if hints := m.PromotionPreviewHints(ctx, sess); len(hints) > 0 {
		out.PromotionPreviewHints = hints
	}
	out.RequiredByWorkflow = m.RequiresPhaseVerify(ctx, sess.ID)
	return out
}

func sourceCheckObservations(records []evidence.Record, boundary time.Time, revision, root string) *SourceCheckObservations {
	out := &SourceCheckObservations{Checks: []SourceCheckObservation{}}
	seen := make(map[string]bool)
	const limit = 6
	for i := len(records) - 1; i >= 0; i-- {
		record := records[i]
		producer, _ := record.Artifacts["producer"].(string)
		check, _ := record.Artifacts["is_check"].(bool)
		if record.TypedGateType() != evidence.GateTypeVerify || !check || (producer != ProducerVerify && producer != ProducerCommand) {
			continue
		}
		id, _ := record.Artifacts["check_id"].(string)
		if id != "" && seen[id] {
			continue
		}
		seen[id] = true
		if len(out.Checks) == limit {
			out.Omitted++
			continue
		}
		command, _ := record.Artifacts["command"].(string)
		cwd, _ := record.Artifacts["cwd"].(string)
		source, _ := record.Artifacts["source_revision"].(string)
		identity, _ := record.Artifacts["source_root_digest"].(string)
		out.Checks = append(out.Checks, SourceCheckObservation{
			CheckID: id, Command: runeclamp.ClampBytes(strings.TrimSpace(command), 240),
			Cwd: runeclamp.ClampBytes(cwd, 160), Verdict: record.GateVerdict,
			Match:       sourceMatch(source, identity, revision, root),
			PriorIntent: record.GateAt().Before(boundary),
		})
	}
	return out
}

func sourceMatch(recordedRevision, recordedRoot, currentRevision, currentRoot string) SourceMatch {
	if recordedRevision == "" || recordedRoot == "" || currentRevision == "" || currentRoot == "" {
		return SourceUnknown
	}
	if recordedRevision == currentRevision && recordedRoot == currentRoot {
		return SourceCurrent
	}
	return SourceChanged
}
