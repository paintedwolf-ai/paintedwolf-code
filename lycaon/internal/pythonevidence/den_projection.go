package pythonevidence

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
)

var (
	// ErrIncompleteScope is returned when a ViewerScope or record lacks
	// required SessionID, Actor, or View authorization identity.
	ErrIncompleteScope = errors.New("authorization scope requires non-empty session_id, actor, and view")
	// ErrAccessDenied is returned when a ViewerScope is not authorized to read
	// the requested execution or retained artifact.
	ErrAccessDenied = errors.New("viewer scope not authorized to access resource")
)

// VisibilityRole controls session visibility scoping for Den activity projections.
// Read-only is a mutation posture, NEVER a cross-session read grant.
type VisibilityRole string

const (
	RoleCoordinator VisibilityRole = "coordinator"
	RoleWorker      VisibilityRole = "worker"
	RoleReadOnly    VisibilityRole = "read_only"
)

// ViewerScope identifies the caller requesting a Den activity projection or
// retained artifact. SessionID, Actor, and View are all mandatory; incomplete
// scopes fail closed.
type ViewerScope struct {
	Role       VisibilityRole `json:"role"`
	SessionID  string         `json:"session_id"`
	CheckoutID string         `json:"checkout_id,omitempty"`
	Actor      string         `json:"actor"`
	View       string         `json:"view"`
}

// ValidateViewerScope checks that scope carries complete authorization identity.
func ValidateViewerScope(scope ViewerScope) error {
	switch scope.Role {
	case RoleCoordinator, RoleWorker, RoleReadOnly:
	default:
		return fmt.Errorf("%w: unknown role %q", ErrIncompleteScope, scope.Role)
	}
	if strings.TrimSpace(scope.SessionID) == "" ||
		strings.TrimSpace(scope.Actor) == "" ||
		strings.TrimSpace(scope.View) == "" {
		return ErrIncompleteScope
	}
	return nil
}

// AuthorizeExecutionAccess enforces fail-closed session, checkout, actor, and
// view isolation. No role—including RoleReadOnly—may cross session boundaries.
func AuthorizeExecutionAccess(exec ExecutionRecord, scope ViewerScope) bool {
	if err := ValidateViewerScope(scope); err != nil {
		return false
	}
	if strings.TrimSpace(exec.SessionID) == "" || exec.SessionID != scope.SessionID {
		return false
	}
	if exec.CheckoutID != "" && scope.CheckoutID != "" && exec.CheckoutID != scope.CheckoutID {
		return false
	}
	switch scope.Role {
	case RoleCoordinator:
		return true
	case RoleWorker:
		return strings.TrimSpace(exec.Actor) != "" &&
			strings.TrimSpace(exec.View) != "" &&
			exec.Actor == scope.Actor &&
			exec.View == scope.View
	case RoleReadOnly:
		// Read-only is a mutation posture, never a cross-session or cross-view grant.
		if exec.CheckoutID != "" && strings.TrimSpace(scope.CheckoutID) == "" {
			return false
		}
		return strings.TrimSpace(exec.View) != "" && exec.View == scope.View
	default:
		return false
	}
}

// ReadMemberProjection preserves an individual inspectable read operation
// inside a grouped ReadGroup.
type ReadMemberProjection struct {
	OperationID           string                           `json:"operation_id"`
	ParentOperationID     string                           `json:"parent_operation_id"`
	Target                pythonruntime.CanonicalTarget    `json:"target"`
	Revision              pythondocuments.DocumentRevision `json:"revision"`
	BytesDeliveredToGuest int64                            `json:"bytes_delivered_to_guest"`
	ReadWitnessCount      int                              `json:"read_witness_count"`
	UnsavedDraft          bool                             `json:"unsaved_draft,omitempty"`
}

// ReadGroup aggregates repeated reads (such as a loop reading N files) into a
// compact Den group while retaining every inspectable member operation.
type ReadGroup struct {
	GroupID             string                  `json:"group_id"`
	ParentOperationID   string                  `json:"parent_operation_id"`
	Namespace           pythonruntime.Namespace `json:"namespace"`
	RootID              string                  `json:"root_id,omitempty"`
	FileCount           int                     `json:"file_count"`
	ReadCount           int                     `json:"read_count"`
	TotalBytesDelivered int64                   `json:"total_bytes_delivered"`
	Members             []ReadMemberProjection  `json:"members"`
}

// PublishedDiffProjection represents a committed workspace change attributed
// explicitly as python_workspace_publication.
type PublishedDiffProjection struct {
	OperationID       string                            `json:"operation_id"`
	ParentOperationID string                            `json:"parent_operation_id"`
	HostPublicationID string                            `json:"host_publication_id"`
	AttributionKind   AttributionKind                   `json:"attribution_kind"`
	Target            pythonruntime.CanonicalTarget     `json:"target"`
	PreviousTarget    *pythonruntime.CanonicalTarget    `json:"previous_target,omitempty"`
	Operation         string                            `json:"operation"`
	BaseRevision      pythondocuments.DocumentRevision  `json:"base_revision"`
	CommittedRevision *pythondocuments.DocumentRevision `json:"committed_revision,omitempty"`
	BytesWritten      int64                             `json:"bytes_written,omitempty"`
	WriteOrigin       AttributedFrame                   `json:"write_origin"`
	FlushClose        AttributedFrame                   `json:"flush_close"`
}

// ConflictProjection identifies a settlement revision conflict with the exact
// reached operation and stale vs current revisions.
type ConflictProjection struct {
	OperationID       string                        `json:"operation_id"`
	ParentOperationID string                        `json:"parent_operation_id"`
	Target            pythonruntime.CanonicalTarget `json:"target"`
	ExpectedRevision  string                        `json:"expected_revision"`
	ActualRevision    string                        `json:"actual_revision"`
	StopCode          pythonruntime.StopCode        `json:"stop_code"`
}

// RefusedChildProjection captures a refused reached operation (for example
// COMMAND_EFFECT_NOT_ALLOWED) for Den inspection.
type RefusedChildProjection struct {
	OperationID       string                        `json:"operation_id"`
	ParentOperationID string                        `json:"parent_operation_id,omitempty"`
	Family            pythonruntime.EffectFamily    `json:"family"`
	Operation         string                        `json:"operation"`
	Target            pythonruntime.CanonicalTarget `json:"target"`
	Decision          pythonruntime.DecisionKind    `json:"decision"`
	StopCode          pythonruntime.StopCode        `json:"stop_code,omitempty"`
	Reason            string                        `json:"reason,omitempty"`
	Frame             AttributedFrame               `json:"frame"`
}

// UncertainExternalProjection surfaces external subprocess or HTTP effects
// whose downstream state is opaque or partially failed.
type UncertainExternalProjection struct {
	OperationID       string   `json:"operation_id"`
	ParentOperationID string   `json:"parent_operation_id"`
	Kind              string   `json:"kind"` // "subprocess" | "http"
	Argv              []string `json:"argv,omitempty"`
	URL               string   `json:"url,omitempty"`
	MediatedView      bool     `json:"mediated_view,omitempty"`
	ObservationLimits string   `json:"observation_limits,omitempty"`
	PartialFailure    bool     `json:"partial_failure,omitempty"`
	ExitCode          int      `json:"exit_code,omitempty"`
	Status            int      `json:"status,omitempty"`
	Uncertain         bool     `json:"uncertain"`
}

// PythonActivityProjection is the Den-ready activity view of an ExecutionLedger.
type PythonActivityProjection struct {
	ExecutionID           string                                  `json:"execution_id"`
	CallID                string                                  `json:"call_id"`
	SessionID             string                                  `json:"session_id"`
	CheckoutID            string                                  `json:"checkout_id,omitempty"`
	Actor                 string                                  `json:"actor"`
	View                  string                                  `json:"view"`
	Surface               string                                  `json:"surface"`
	RuntimeImageID        string                                  `json:"runtime_image_id"`
	VisibilityRole        VisibilityRole                          `json:"visibility_role"`
	Visible               bool                                    `json:"visible"`
	RedactedForVisibility bool                                    `json:"redacted_for_visibility,omitempty"`
	ReadOnlyView          bool                                    `json:"read_only_view,omitempty"`
	CleanCompletion       bool                                    `json:"clean_completion"`
	ExitCode              int                                     `json:"exit_code"`
	Stop                  *pythonruntime.TerminalStop             `json:"stop,omitempty"`
	ReadGroups            []ReadGroup                             `json:"read_groups,omitempty"`
	PublishedDiffs        []PublishedDiffProjection               `json:"published_diffs,omitempty"`
	Conflicts             []ConflictProjection                    `json:"conflicts,omitempty"`
	RefusedChildren       []RefusedChildProjection                `json:"refused_children,omitempty"`
	UncertainExternals    []UncertainExternalProjection           `json:"uncertain_externals,omitempty"`
	PreviewListeners      []pythonadmission.PreviewListenerRecord `json:"preview_listeners,omitempty"`
	ScreenedOutputs       []ScreenedModelOutputFact               `json:"screened_outputs,omitempty"`
	NativeSourceRanges    []NativeSourceRangeFact                 `json:"native_source_ranges,omitempty"`
}

// ProjectForDen builds a visibility-scoped Den activity projection from ledger.
// Fails closed when scope is incomplete or unauthorized.
func ProjectForDen(ledger *ExecutionLedger, scope ViewerScope) PythonActivityProjection {
	exec := ledger.Execution()
	if !AuthorizeExecutionAccess(exec, scope) {
		return PythonActivityProjection{
			ExecutionID:           exec.ExecutionID,
			VisibilityRole:        scope.Role,
			Visible:               false,
			RedactedForVisibility: true,
		}
	}

	proj := PythonActivityProjection{
		ExecutionID:        exec.ExecutionID,
		CallID:             exec.CallID,
		SessionID:          exec.SessionID,
		CheckoutID:         exec.CheckoutID,
		Actor:              exec.Actor,
		View:               exec.View,
		Surface:            exec.Surface,
		RuntimeImageID:     exec.RuntimeImageID,
		VisibilityRole:     scope.Role,
		Visible:            true,
		CleanCompletion:    exec.CleanCompletion,
		ExitCode:           exec.ExitCode,
		Stop:               exec.Stop,
		ReadGroups:         groupReads(ledger.DocumentReads()),
		RefusedChildren:    projectRefusals(ledger.Admissions()),
		UncertainExternals: projectUncertainExternals(ledger.Subprocesses(), ledger.HTTPRecords()),
		ScreenedOutputs:    ledger.ScreenedModelOutputs(),
		NativeSourceRanges: ledger.ModelSourceCitations(),
	}

	diffs, conflicts := projectPublications(ledger.Publications())
	proj.Conflicts = conflicts

	if scope.Role == RoleReadOnly {
		proj.ReadOnlyView = true
		return proj
	}

	proj.PublishedDiffs = diffs
	for _, pl := range ledger.PreviewListeners() {
		proj.PreviewListeners = append(proj.PreviewListeners, pl.Record)
	}
	return proj
}

type readGroupKey struct {
	parentOpID string
	namespace  pythonruntime.Namespace
	rootID     string
}

func groupReads(reads []LedgerDocumentRead) []ReadGroup {
	if len(reads) == 0 {
		return nil
	}
	order := make([]readGroupKey, 0)
	byKey := make(map[readGroupKey]*ReadGroup)
	filesByKey := make(map[readGroupKey]map[string]struct{})

	for _, r := range reads {
		k := readGroupKey{
			parentOpID: r.ParentOperationID,
			namespace:  r.Record.Target.Namespace,
			rootID:     r.Record.Target.RootID,
		}
		grp, exists := byKey[k]
		if !exists {
			order = append(order, k)
			grp = &ReadGroup{
				GroupID:           fmt.Sprintf("reads:%s:%s:%s", k.parentOpID, k.namespace, k.rootID),
				ParentOperationID: k.parentOpID,
				Namespace:         k.namespace,
				RootID:            k.rootID,
			}
			byKey[k] = grp
			filesByKey[k] = make(map[string]struct{})
		}
		grp.ReadCount++
		grp.TotalBytesDelivered += r.Record.BytesDeliveredToGuest
		filesByKey[k][r.Record.Target.RelPath] = struct{}{}
		grp.FileCount = len(filesByKey[k])
		grp.Members = append(grp.Members, ReadMemberProjection{
			OperationID:           r.Record.OperationID,
			ParentOperationID:     r.ParentOperationID,
			Target:                r.Record.Target,
			Revision:              r.Record.Revision,
			BytesDeliveredToGuest: r.Record.BytesDeliveredToGuest,
			ReadWitnessCount:      r.ReadWitnessCount,
			UnsavedDraft:          r.Record.UnsavedDraft,
		})
	}

	out := make([]ReadGroup, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out
}

func projectPublications(pubs []LedgerPublication) ([]PublishedDiffProjection, []ConflictProjection) {
	var diffs []PublishedDiffProjection
	var conflicts []ConflictProjection
	for _, p := range pubs {
		if p.Record.Conflict != nil {
			conflicts = append(conflicts, ConflictProjection{
				OperationID:       p.Record.OperationID,
				ParentOperationID: p.ParentOperationID,
				Target:            p.Record.Conflict.Target,
				ExpectedRevision:  p.Record.Conflict.ExpectedRevision,
				ActualRevision:    p.Record.Conflict.ActualRevision,
				StopCode:          pythonruntime.StopRevisionConflict,
			})
			continue
		}
		diffs = append(diffs, PublishedDiffProjection{
			OperationID:       p.Record.OperationID,
			ParentOperationID: p.ParentOperationID,
			HostPublicationID: p.Attribution.HostPublicationID,
			AttributionKind:   p.Attribution.AttributionKind,
			Target:            p.Record.Target,
			PreviousTarget:    p.Record.PreviousTarget,
			Operation:         p.Record.Operation,
			BaseRevision:      p.Record.BaseRevision,
			CommittedRevision: p.Record.CommittedRevision,
			BytesWritten:      p.Record.BytesWritten,
			WriteOrigin:       p.Attribution.WriteOriginFrame,
			FlushClose:        p.Attribution.FlushCloseFrame,
		})
	}
	return diffs, conflicts
}

func projectRefusals(admissions []LedgerAdmission) []RefusedChildProjection {
	var out []RefusedChildProjection
	for _, a := range admissions {
		if a.Record.Decision != pythonruntime.DecisionDeny && a.Record.Decision != pythonruntime.DecisionStop {
			continue
		}
		out = append(out, RefusedChildProjection{
			OperationID:       a.Record.OperationID,
			ParentOperationID: a.ParentOperationID,
			Family:            a.Record.Family,
			Operation:         a.Record.Operation,
			Target:            a.Record.AuthoritativeTarget,
			Decision:          a.Record.Decision,
			StopCode:          a.Record.StopCode,
			Reason:            a.Record.Reason,
			Frame:             a.Frame,
		})
	}
	return out
}

func projectUncertainExternals(subprocs []LedgerSubprocess, https []LedgerHTTP) []UncertainExternalProjection {
	var out []UncertainExternalProjection
	for _, sp := range subprocs {
		if sp.UncertainOutcome {
			out = append(out, UncertainExternalProjection{
				OperationID:       sp.Record.OperationID,
				ParentOperationID: sp.ParentOperationID,
				Kind:              "subprocess",
				Argv:              append([]string(nil), sp.Record.Argv...),
				MediatedView:      sp.Record.MediatedView,
				ObservationLimits: sp.Record.ObservationLimits,
				ExitCode:          sp.Record.ExitCode,
				Uncertain:         true,
			})
		}
	}
	for _, h := range https {
		if !h.Record.PartialFailure && h.Record.Error == "" {
			continue
		}
		out = append(out, UncertainExternalProjection{
			OperationID:       h.Record.OperationID,
			ParentOperationID: h.ParentOperationID,
			Kind:              "http",
			URL:               h.Record.URL,
			PartialFailure:    h.Record.PartialFailure,
			Status:            h.Record.Status,
			Uncertain:         true,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].OperationID < out[j].OperationID
	})
	return out
}
