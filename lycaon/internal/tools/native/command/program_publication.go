package command

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// publishAcceptedDocuments is entered only by an admitted program owner. Its
// preparation writes use separate native admission and receipts, not the read's
// permission. The direct InvokeNative below does not reenter this once barrier.
func (d *ProgramDispatcher) publishAcceptedDocuments(ctx context.Context, parent toolcontract.OperationInvocation) error {
	d.run.publicationOnce.Do(func() {
		d.run.publicationErr = preparationFailure(d.publishDocuments(ctx, parent), toolcontract.NoEffect)
	})
	return d.run.publicationErr
}

func (d *ProgramDispatcher) publishDocuments(ctx context.Context, parent toolcontract.OperationInvocation) error {
	documents, ok := tools.ProjectDocuments(d.run.tc)
	if !ok {
		return nil
	}
	mutation, _ := toolcontract.NewCommandEffects(toolcontract.CommandMutation)
	if !d.run.surface.AllowsEffects(mutation) {
		return nil
	}
	write, ok := d.run.registry.Operation("write")
	if !ok {
		return fmt.Errorf("accepted document publication requires the write owner")
	}
	definition := write.Operation
	bound, err := toolcontract.BindPublication(definition, parent, toolcontract.AcceptedDocumentPublication, "")
	if err != nil {
		return err
	}
	if err := d.run.surface.ValidateOperation(bound); err != nil {
		//nolint:nilerr // Surfaces without write authority skip accepted-document publication.
		return nil
	}
	seen := map[string]bool{}
	for _, root := range d.run.tc.Source.Roots {
		branch, err := d.run.tc.SourceBranch(root.ID)
		if err != nil {
			return err
		}
		if seen[branch.String()] {
			continue
		}
		seen[branch.String()] = true
		err = documents.VisitAgentPublications(ctx, d.run.tc.Identity.ProjectID, branch, d.run.tc.Identity.SessionID, d.run.tc.Identity.UserTurn, d.run.tc.Identity.ToolCallID, func(candidate tools.EditorDocumentPublicationCandidate) error {
			release, err := d.run.memory.ReserveProgramState(ctx, candidate.DraftBytes+candidate.BaseBytes+2048)
			if err != nil {
				return commandDocumentBudgetError(err)
			}
			defer release()
			publication, err := candidate.Load(ctx)
			if err != nil {
				return err
			}
			for _, attached := range d.run.tc.Source.Roots {
				if attached.ID == publication.RootID {
					publication.Path = filepath.Join(attached.Path, filepath.FromSlash(publication.Path))
					break
				}
			}
			return d.publishDocument(ctx, parent, definition, publication)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *ProgramDispatcher) publishDocument(ctx context.Context, parent toolcontract.OperationInvocation, definition toolcontract.OperationDefinition, publication tools.EditorDocumentPublication) error {
	if d.run.publishDocument == nil {
		return fmt.Errorf("accepted document publication requires the write owner")
	}
	invocationID := parent
	invocationID.Identity.ExecutionID = uuid.NewString()
	invocationID.Identity.ParentExecutionID = parent.Identity.ExecutionID
	invocationID.Scope.RootID = publication.RootID
	branch, err := d.run.tc.SourceBranch(publication.RootID)
	if err != nil {
		return err
	}
	invocationID.Scope.BranchID = branch.String()
	bound, err := toolcontract.BindPublication(definition, invocationID, toolcontract.AcceptedDocumentPublication, "")
	if err != nil {
		return err
	}
	arguments := map[string]any{"path": publication.Path, "content": publication.Document.Text}
	recorded := map[string]any{"path": publication.Path, "content": publication.Document.Text, "preparation_kind": "accepted_document_publication", "document_id": publication.Document.ID, "accepted_revision": publication.Document.Revision}
	child, err := d.prepareInvocation(ctx, definition, invocationID, publication.Path, recorded, nil)
	if err != nil {
		return err
	}
	defer child.release()
	var saved tools.EditorDocumentApplied
	effect := toolexecution.NativeEffect{Operation: bound, Arguments: arguments, Inspection: d.run.capture,
		Stage: toolcontract.ExecutionStage{ExecutionID: invocationID.Identity.ExecutionID, OperationID: definition.Facts().Identity, Argv: []string{definition.Facts().Identity, publication.Path}},
		PrepareReviewed: func(_ context.Context, values map[string]any, _ tools.ToolContext) (toolexecution.PreparedNativeEffect, error) {
			if values["path"] != publication.Path || values["content"] != publication.Document.Text {
				return toolexecution.PreparedNativeEffect{}, &toolrejection.ToolReject{Code: toolrejection.ToolOwnerFailedCode, FailureClass: "owner_error", OwnerRef: definition.Facts().Contract.Owner, Data: map[string]any{"tool": definition.Facts().Identity, "path": publication.Path, "reviewed_inputs_changed": true}}
			}
			return toolexecution.PreparedNativeEffect{Operation: bound, InvokeOwner: func(ctx context.Context, tc tools.ToolContext, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
				var err error
				saved, err = d.run.publishDocument(ctx, tc, d.run.memory, publication)
				if err != nil {
					certainty := toolcontract.EffectUnknown
					if errors.Is(err, tools.ErrEditorDocumentMoved) || errors.Is(err, tools.ErrEditorExecutionConflict) {
						certainty = toolcontract.NoEffect
					}
					err = preparationFailure(err, certainty)
					return ownerFailure(err), err
				}
				return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.EffectCompleted}, nil
			}}, nil
		},
	}
	marker := &invocation.ProgramAdmissionRecorder{Recorder: d.run.recorder, Inputs: d.run.capture}
	result, runErr := d.run.executor.InvokeNative(ctx, d.run.frame, d.run.surface, d.run.memory, d.run.progress, marker, effect, d.run.tc)
	if toolrejection.AsToolReject(runErr) != nil {
		d.shared.mu.Lock()
		if d.shared.rejected == nil {
			d.shared.rejected = &nativeRejection{operation: bound, cause: runErr}
		}
		d.shared.mu.Unlock()
	}
	settlement := toolcontract.ProgramChildSettlement{Result: result.Producer, OwnerInvoked: result.OwnerInvoked || marker.OwnerStartRecorded, HostStopCode: result.HostStopCode}
	if saved.Saved {
		settlement.AcceptedRevision = fmt.Sprintf("document:%s@%d", publication.Document.ID, publication.Document.Revision)
		settlement.PublishedRevision = "sha256:" + saved.Document.BaseSHA256
	}
	return errors.Join(runErr, d.settleWith(ctx, child, settlement))
}

// A preparation error prevents the reached producer, even when a lower owner
// has no typed refusal. Its own receipt retains any uncertain save effect.
type publicationFailure struct {
	cause     error
	reject    *toolrejection.ToolReject
	certainty toolcontract.EffectCertainty
}

func (e *publicationFailure) Error() string   { return toolrejection.ToolOwnerFailedCode }
func (e *publicationFailure) Unwrap() []error { return []error{e.cause, e.reject} }
func (e *publicationFailure) ProducerResult() toolcontract.ProducerResult {
	return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: toolrejection.ToolOwnerFailedCode, EffectCertainty: e.certainty}
}

func preparationFailure(err error, certainty toolcontract.EffectCertainty) error {
	if err == nil {
		return nil
	}
	var domain toolcontract.ProducerFailure
	if errors.As(err, &domain) || toolrejection.AsToolReject(err) != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &publicationFailure{cause: err, certainty: certainty, reject: &toolrejection.ToolReject{Code: toolrejection.ToolOwnerFailedCode, FailureClass: "owner_error", Data: map[string]any{"tool": "write", "preparation_kind": "accepted_document_publication"}}}
}
