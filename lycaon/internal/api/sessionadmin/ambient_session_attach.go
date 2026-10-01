package sessionadmin

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// AttachAmbientOnSessionCreate attaches the bundled ambient workflow to a
// build-posture session. Fails closed: a build session without a leaf run has no
// phase, no gates, and no agent roster.
func (s *Handler) AttachAmbientOnSessionCreate(ctx context.Context, req wire.CreateSessionRequest, sessionID string) error {
	if req.Posture != wire.SessionPostureBuild {
		return nil
	}
	// The registry config naming the ambient workflow ships in the binary.
	ref, err := workflowdef.LoadRegistryConfig(extpacks.Bundled(config.PlatformFlows))
	if err != nil {
		return fmt.Errorf("attach ambient workflow: resolve default: %w", err)
	}
	run, err := s.Workflows.StartAmbient(ctx, sessionID, ref.ID, ref.Version)
	if err != nil {
		return fmt.Errorf("attach ambient workflow %s@%s: %w", ref.ID, ref.Version, err)
	}
	if run == nil {
		return fmt.Errorf("attach ambient workflow %s@%s: started no run", ref.ID, ref.Version)
	}
	return nil
}
