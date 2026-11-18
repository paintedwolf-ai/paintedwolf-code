package sessionadmin

import (
	"context"
	"fmt"

	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Lifecycle) AttachAmbientOnSessionCreate(ctx context.Context, req wire.CreateSessionRequest, sessionID string) error {
	if req.Posture != wire.SessionPostureBuild {
		return nil
	}
	if _, err := s.Workflows.Ambient.EnsureSessionWorkflow(ctx, sessionID); err != nil {
		return fmt.Errorf("attach ambient workflow: %w", err)
	}
	return nil
}
