package portablepython

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonevidence"
)

// DenProjectionForSession projects a persisted execution for Den/Files
// inspection under the caller's ViewerScope through this Service's durable
// ReceiptSink.
func (s *Service) DenProjectionForSession(executionID string, scope pythonevidence.ViewerScope) (pythonevidence.PythonActivityProjection, error) {
	if s == nil {
		return pythonevidence.PythonActivityProjection{}, errors.New("portable python service is not initialized")
	}
	projector, ok := s.receiptSink.(interface {
		ProjectPersistedForDen(string, pythonevidence.ViewerScope) (pythonevidence.PythonActivityProjection, error)
	})
	if !ok || projector == nil {
		return pythonevidence.PythonActivityProjection{}, errors.New("durable receipt store does not support Den projection")
	}
	return projector.ProjectPersistedForDen(executionID, scope)
}

// ReadRetainedOutputForSession enforces fail-closed ViewerScope authorization
// and reads disk-backed retained output bytes through this Service's durable
// ReceiptSink and RetainedStreamPort.
func (s *Service) ReadRetainedOutputForSession(ctx context.Context, scope pythonevidence.ViewerScope, artifactID string) ([]byte, error) {
	if s == nil {
		return nil, errors.New("portable python service is not initialized")
	}
	reader, ok := s.receiptSink.(interface {
		ReadRetainedBytes(context.Context, pythonevidence.ViewerScope, string, pythonevidence.RetainedStreamPort) ([]byte, error)
	})
	if !ok || reader == nil {
		return nil, errors.New("durable receipt store does not support retained output reads")
	}
	return reader.ReadRetainedBytes(ctx, scope, artifactID, s.streamPort)
}

// PersistedAdmissionsForExecution loads durable LedgerAdmission records for
// executionID through this Service's configured ReceiptSink.
func (s *Service) PersistedAdmissionsForExecution(executionID string) ([]pythonevidence.LedgerAdmission, error) {
	if s == nil {
		return nil, errors.New("portable python service is not initialized")
	}
	loader, ok := s.receiptSink.(interface {
		LoadAdmissions(string) ([]pythonevidence.LedgerAdmission, error)
	})
	if !ok || loader == nil {
		return nil, errors.New("durable receipt store does not support admission queries")
	}
	return loader.LoadAdmissions(executionID)
}

// finalizeResponse screens stdout/stderr/stop text for secrets, enforces the
// inline output budget (retaining overflow in RetainedArtifactStore), records
// execution/admission/document/publication evidence into pythonevidence, and
// attaches a non-nil Den-ready EvidenceSummary to resp.
func (s *Service) finalizeResponse(
	ctx context.Context,
	req ToolRequest,
	resp *ToolResponse,
	memAdm *pythonadmission.MemoryEvidenceStore,
	memDoc *pythondocuments.EvidenceLog,
) {
	if resp == nil {
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = "default"
	}
	actor := strings.TrimSpace(req.Actor)
	if actor == "" {
		actor = "agent"
	}
	view := strings.TrimSpace(req.View)
	if view == "" {
		view = "workspace"
	}
	parentOpID := strings.TrimSpace(req.CallID)
	if parentOpID == "" {
		parentOpID = resp.ExecutionID
	}

	budget := req.InlineBudgetBytes
	if budget <= 0 {
		budget = s.inlineBudget
	}
	if budget <= 0 {
		budget = DefaultInlineBudgetBytes
	}
	resp.InlineBudgetBytes = budget

	screenedStdout, redStdout := pythonevidence.ScreenText(s.screener, resp.Stdout)
	screenedStderr, redStderr := pythonevidence.ScreenText(s.screener, resp.Stderr)
	redactedTotal := redStdout + redStderr

	if resp.Stop != nil && resp.Stop.Reason != "" {
		cp := *resp.Stop
		screenedReason, redStop := pythonevidence.ScreenText(s.screener, cp.Reason)
		cp.Reason = screenedReason
		redactedTotal += redStop
		resp.Stop = &cp
	}

	// Enforce inline budget and retain overflow in RetainedArtifactStore.
	if len(screenedStdout) > budget {
		resp.StdoutTruncated = true
		if s.retainedStore != nil {
			if summary, err := s.retainedStore.Put(ctx, pythonevidence.PutRequest{
				SessionID:         sessionID,
				CheckoutID:        req.CheckoutID,
				Actor:             actor,
				View:              view,
				ExecutionID:       resp.ExecutionID,
				ParentOperationID: parentOpID,
				Producer:          pythonevidence.ProducerPython,
				MediaType:         "text/plain",
				Data:              []byte(screenedStdout),
			}); err == nil {
				resp.RetainedStdoutArtifactID = summary.ArtifactID
				if persist, ok := s.receiptSink.(interface {
					PersistRetainedArtifact(context.Context, pythonevidence.RetainedArtifactSummary) error
				}); ok {
					_ = persist.PersistRetainedArtifact(ctx, summary)
				}
			}
		}
		resp.Stdout = screenedStdout[:budget]
	} else {
		resp.Stdout = screenedStdout
	}

	if len(screenedStderr) > budget {
		resp.StderrTruncated = true
		if s.retainedStore != nil {
			if summary, err := s.retainedStore.Put(ctx, pythonevidence.PutRequest{
				SessionID:         sessionID,
				CheckoutID:        req.CheckoutID,
				Actor:             actor,
				View:              view,
				ExecutionID:       resp.ExecutionID,
				ParentOperationID: parentOpID,
				Producer:          pythonevidence.ProducerPython,
				MediaType:         "text/plain",
				Data:              []byte(screenedStderr),
			}); err == nil {
				resp.RetainedStderrArtifactID = summary.ArtifactID
			}
		}
		resp.Stderr = screenedStderr[:budget]
	} else {
		resp.Stderr = screenedStderr
	}
	resp.RedactedCount = redactedTotal

	// Populate pythonevidence.ExecutionLedger and project EvidenceSummary.
	submittedSource := req.Code
	if submittedSource == "" {
		submittedSource = req.Script
	}
	if submittedSource == "" {
		submittedSource = req.Module
	}
	execRec := pythonevidence.ExecutionRecord{
		ExecutionID:     resp.ExecutionID,
		CallID:          parentOpID,
		SessionID:       sessionID,
		CheckoutID:      req.CheckoutID,
		Actor:           actor,
		View:            view,
		Surface:         req.Surface,
		SubmittedSource: submittedSource,
		RuntimeImageID:  s.runtimeManifest.ImageID,
		StartedAt:       s.clock.Now().UTC(),
	}
	var ledger *pythonevidence.ExecutionLedger
	if s.receiptSink != nil {
		if prodLedger, err := pythonevidence.NewProductionExecutionLedger(ctx, execRec, s.screener, s.receiptSink, s.clock.Now); err == nil {
			ledger = prodLedger
		}
	}
	if ledger == nil {
		ledger = pythonevidence.NewExecutionLedger(execRec, s.screener, s.clock.Now)
	}

	if memAdm != nil {
		for _, adm := range memAdm.Admissions() {
			ledger.RecordAdmission(adm, parentOpID, nil)
		}
		for _, h := range memAdm.HTTPRecords() {
			_ = ledger.RecordHTTP(h, parentOpID)
		}
		for _, pl := range memAdm.PreviewListeners() {
			_ = ledger.RecordPreviewListener(pl, parentOpID)
		}
		for _, sub := range memAdm.Subprocesses() {
			_ = ledger.RecordSubprocess(sub, parentOpID)
		}
	}
	if memDoc != nil {
		for _, r := range memDoc.Reads() {
			witnesses := 0
			if r.BytesDeliveredToGuest > 0 {
				witnesses = 1
			}
			_ = ledger.RecordDocumentRead(r, parentOpID, witnesses, nil, nil)
		}
		for _, ev := range memDoc.OverlayEvents() {
			_ = ledger.RecordOverlayEvent(ev, parentOpID, nil, nil)
		}
		for i, pub := range memDoc.Publications() {
			pubID := fmt.Sprintf("pub-%s-%d", resp.ExecutionID, i+1)
			_ = ledger.RecordPublication(pub, parentOpID, pubID, pythonevidence.AttributionPythonWorkspacePublication, nil, nil)
		}
	}

	if resp.Stdout != "" || resp.RetainedStdoutArtifactID != "" {
		ledger.RecordDerivedOutput("stdout", resp.Stdout, resp.RetainedStdoutArtifactID)
		ledger.RecordScreenedModelOutput(resp.Stdout)
	}
	if resp.Stderr != "" || resp.RetainedStderrArtifactID != "" {
		ledger.RecordDerivedOutput("stderr", resp.Stderr, resp.RetainedStderrArtifactID)
	}
	ledger.RecordCompletion(resp.CleanCompletion, resp.ExitCode, resp.Stop)

	scope := pythonevidence.ViewerScope{
		Role:       pythonevidence.RoleCoordinator,
		SessionID:  sessionID,
		CheckoutID: req.CheckoutID,
		Actor:      actor,
		View:       view,
	}
	proj := pythonevidence.ProjectForDen(ledger, scope)
	resp.EvidenceSummary = &proj
}
