package app

import (
	"context"
	"github.com/lycaon/lycaon/internal/secretcap"
	workflowinputs "github.com/lycaon/lycaon/internal/workflow/inputs"
)

func (b *serveBuilder) wireSecretCapabilities() error {
	if b.security.Capabilities != nil {
		return nil
	}
	if err := b.security.BuildCapabilities(b.execution.Host.Registry, b.execution.Host.Executor.Secrets); err != nil {
		return err
	}
	service := b.security.Capabilities
	if b.workflows != nil && b.workflows.Manager != nil {
		b.workflows.Manager.Asks.SetSecretCapture(func(ctx context.Context, req workflowinputs.SecretCaptureRequest) (workflowinputs.SecretCaptureResult, error) {
			put, err := service.Put(ctx, secretcap.PutRequest{
				ProjectID: req.ProjectID, ChatSessionID: req.RootSessionID, SessionID: req.SessionID,
				OperationID: req.OperationID, Name: req.Name, Purpose: req.Purpose, Scope: req.Scope,
				Origin: secretcap.OriginAskUserResponse, Value: req.Value, PersonID: req.PersonID, AgentUseTTL: req.AgentUseTTL,
			})
			result := workflowinputs.SecretCaptureResult{Reference: put.Metadata.Reference}
			// A committed mint needs compensating even when Put errs; only this reference reaches its bytes.
			if put.Created {
				result.Discard = func(discardCtx context.Context) error {
					return service.DiscardCreated(discardCtx, req.ProjectID, put.Metadata.Reference)
				}
			}
			return result, err
		})
	}
	return nil
}
