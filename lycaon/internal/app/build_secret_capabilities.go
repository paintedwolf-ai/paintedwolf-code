package app

import (
	"context"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/workflow"
)

func (b *serveBuilder) wireSecretCapabilities() error {
	if b.secretCaps != nil {
		return nil
	}
	remember := func(rootSessionID string, values []secretmatch.Remembered) {
		if b.secretHarvest != nil {
			b.secretHarvest.Remember(rootSessionID, values...)
		}
	}
	service, err := secretcap.New(b.db, remember)
	if err != nil {
		return fmt.Errorf("secret capability store: %w", err)
	}
	if err := service.ConfigureRevealPublicKey(os.Getenv(secretcap.RevealPublicKeyEnv)); err != nil {
		return fmt.Errorf("configure managed secret reveal: %w", err)
	}
	if err := service.Reconcile(b.ctx); err != nil {
		return fmt.Errorf("reconcile secret capabilities: %w", err)
	}
	if err := native.RegisterSecretCapabilityTools(b.toolRuntime.Registry, service); err != nil {
		return fmt.Errorf("secret capability tools: %w", err)
	}
	b.toolRuntime.Executor.SetSecretResolver(service)
	if b.workflowMgr != nil {
		b.workflowMgr.SetSecretCapture(func(ctx context.Context, req workflow.SecretCaptureRequest) (workflow.SecretCaptureResult, error) {
			put, err := service.Put(ctx, secretcap.PutRequest{
				ProjectID: req.ProjectID, ChatSessionID: req.RootSessionID, SessionID: req.SessionID,
				OperationID: req.OperationID, Name: req.Name, Purpose: req.Purpose, Scope: req.Scope,
				Origin: secretcap.OriginAskUserResponse, Value: req.Value, PersonID: req.PersonID, AgentUseTTL: req.AgentUseTTL,
			})
			result := workflow.SecretCaptureResult{Reference: put.Metadata.Reference}
			// A committed mint needs compensating even when Put errs; only this reference reaches its bytes.
			if put.Created {
				result.Discard = func(discardCtx context.Context) error {
					return service.DiscardCreated(discardCtx, req.ProjectID, put.Metadata.Reference)
				}
			}
			return result, err
		})
	}
	b.secretCaps = service
	return nil
}
