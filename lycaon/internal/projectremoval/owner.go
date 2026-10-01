package projectremoval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

var (
	ErrAssessmentChanged = errors.New("project removal evidence changed; review it again")
	ErrInvalidSelection  = errors.New("only extensions assessed as eligible may be selected")
	ErrOperationConflict = errors.New("operation id was already used for another removal request")
	ErrOperationNotFound = errors.New("project removal operation not found")
)

type ProjectReader interface {
	Get(context.Context, string) (*project.Project, error)
	List(context.Context) ([]project.Project, error)
}

// Failure carries structured lifecycle blockers.
type Failure struct {
	Code                                   wire.ApiErrorCode
	Message                                string
	Details                                map[string]any
	Documents, Sessions, Workers, Overlays int
}

func (f *Failure) Error() string { return f.Message }

// Owner settles deletion before optional cleanup and never replays interrupted effects.
type Owner struct {
	Projects         ProjectReader
	Extensions       *extensionstate.Owner
	SuggestionsApply func(project.Project) bool
	Delete           func(context.Context, string, bool) error
	Store            *Store
	mu               sync.Mutex
}

func (o *Owner) Result(ctx context.Context, projectID, operationID string) (wire.ProjectRemovalResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	row, found, err := o.Store.read(ctx, operationID)
	if err != nil {
		return wire.ProjectRemovalResult{}, err
	}
	if !found || row.ProjectID != projectID {
		return wire.ProjectRemovalResult{}, ErrOperationNotFound
	}
	return o.replay(ctx, row)
}

func (o *Owner) replay(ctx context.Context, row record) (wire.ProjectRemovalResult, error) {
	if row.Settled {
		return row.Result, nil
	}
	row.Result.CleanupState = "interrupted"
	row.Result.FailureCode = "removal_interrupted"
	row.Result.Reason = "Removal was interrupted. Review the project and extension settings before starting another operation."
	return row.Result, o.Store.save(ctx, row.Result, true)
}

func (o *Owner) Remove(ctx context.Context, projectID string, req wire.ProjectRemovalRequest) (wire.ProjectRemovalResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, err := uuid.Parse(req.OperationID); err != nil {
		return wire.ProjectRemovalResult{}, fmt.Errorf("invalid operation id: %w", err)
	}
	req.RemoveExtensions = append([]string{}, req.RemoveExtensions...)
	sort.Strings(req.RemoveExtensions)
	encoded, err := json.Marshal(req)
	if err != nil {
		return wire.ProjectRemovalResult{}, err
	}
	row, found, err := o.Store.read(ctx, req.OperationID)
	if err != nil {
		return wire.ProjectRemovalResult{}, err
	}
	if found {
		if row.ProjectID != projectID || row.Request != string(encoded) {
			return wire.ProjectRemovalResult{}, ErrOperationConflict
		}
		return o.replay(ctx, row)
	}
	assessment, err := o.reviewSelection(ctx, projectID, req)
	if err != nil {
		return wire.ProjectRemovalResult{}, err
	}
	result := wire.ProjectRemovalResult{OperationID: req.OperationID, ProjectID: projectID, ProjectState: "unknown", CleanupState: "not_requested", Extensions: append([]string{}, req.RemoveExtensions...)}
	if len(req.RemoveExtensions) > 0 {
		result.Assessment = &assessment
	}
	if err := o.Store.begin(ctx, record{ProjectID: projectID, Request: string(encoded), Result: result}); err != nil {
		return result, err
	}
	// Accepted work settles even if its HTTP caller disconnects.
	ctx = context.WithoutCancel(ctx)
	if err := o.Delete(ctx, projectID, req.Force); err != nil {
		slog.WarnContext(ctx, "project removal did not complete", "project_id", projectID, "operation_id", req.OperationID, "err", err)
		result = o.deletionFailure(ctx, result, err)
		return result, o.Store.save(ctx, result, true)
	}
	result.ProjectState = "deleted"
	if len(req.RemoveExtensions) == 0 {
		return result, o.Store.save(ctx, result, true)
	}
	result.CleanupState = "interrupted"
	if err := o.Store.save(ctx, result, false); err != nil {
		return result, err
	}
	result = o.cleanup(ctx, assessment, req, result)
	return result, o.Store.save(ctx, result, true)
}

func (o *Owner) deletionFailure(ctx context.Context, result wire.ProjectRemovalResult, cause error) wire.ProjectRemovalResult {
	result.CleanupState, result.FailureCode = "retained", "project_removal_failed"
	_, err := o.Projects.Get(ctx, result.ProjectID)
	switch {
	case errors.Is(err, project.ErrNotFound):
		result.ProjectState = "deleted"
		result.Reason = "The project is no longer registered. Extensions were kept because this removal did not complete."
	case err != nil:
		result.ProjectState = "unknown"
		result.Reason = "The project removal outcome could not be confirmed. Extensions were kept."
	default:
		result.ProjectState = "retained"
		result.Reason = "The project could not be deleted. Extensions were kept."
		var failure *Failure
		if errors.As(cause, &failure) {
			result.FailureCode, result.Reason = string(failure.Code), failure.Message
			result.Documents, result.Sessions, result.Workers, result.Overlays = failure.Documents, failure.Sessions, failure.Workers, failure.Overlays
		}
	}
	return result
}

func (o *Owner) reviewSelection(ctx context.Context, projectID string, req wire.ProjectRemovalRequest) (wire.ProjectRemovalAssessment, error) {
	if len(req.RemoveExtensions) > 256 {
		return wire.ProjectRemovalAssessment{}, ErrInvalidSelection
	}
	if _, err := o.Projects.Get(ctx, projectID); err != nil {
		return wire.ProjectRemovalAssessment{}, err
	}
	if len(req.RemoveExtensions) == 0 {
		return wire.ProjectRemovalAssessment{}, nil
	}
	assessment, err := o.assess(ctx, projectID)
	if err != nil {
		return assessment, err
	}
	if assessment.AssessmentToken != req.AssessmentToken {
		return assessment, ErrAssessmentChanged
	}
	eligible := map[string]bool{}
	for _, pack := range assessment.Extensions {
		eligible[pack.PackID] = pack.Disposition == "eligible"
	}
	for _, id := range req.RemoveExtensions {
		if !eligible[id] {
			return assessment, ErrInvalidSelection
		}
		delete(eligible, id)
	}
	return assessment, nil
}

func (o *Owner) cleanup(ctx context.Context, assessment wire.ProjectRemovalAssessment, req wire.ProjectRemovalRequest, result wire.ProjectRemovalResult) wire.ProjectRemovalResult {
	// Reference evidence and device publication have separate revisions.
	refs, err := o.referenceEvidence(ctx, result.ProjectID)
	if err == nil {
		expected := digest(result.ProjectID, assessment.ExtensionRevision, refs.Revision)
		if expected != req.AssessmentToken || !refs.Complete {
			err = ErrAssessmentChanged
		}
	}
	var applied extensionstate.Result
	if err == nil {
		applied, err = o.Extensions.Apply(ctx, extensionstate.Intent{Scope: extensionstate.Scope{Kind: "device"}, ExpectedRevision: assessment.ExtensionRevision, Op: extensionstate.RemoveReviewedOp{
			PackIDs: req.RemoveExtensions,
			Revalidate: func(ctx context.Context) error {
				current, readErr := o.referenceEvidence(ctx, result.ProjectID)
				if readErr != nil {
					return readErr
				}
				if current.Revision != refs.Revision || !current.Complete {
					return ErrAssessmentChanged
				}
				return nil
			},
		}})
	}
	if err == nil {
		result.CleanupState = "removed"
		result.Reason = strings.Join(applied.Warnings, "\n")
		return result
	}
	slog.WarnContext(ctx, "project extension cleanup did not complete", "project_id", result.ProjectID, "operation_id", result.OperationID, "err", err)
	result.CleanupState, result.Reason = "failed", "The project was deleted, but extension cleanup did not complete. Review extension settings before retrying cleanup."
	var stale *extensionstate.StaleError
	if errors.Is(err, ErrAssessmentChanged) || errors.As(err, &stale) {
		result.CleanupState, result.Reason = "retained", "The project was deleted. Extension evidence changed, so extensions were kept."
	}
	return result
}
