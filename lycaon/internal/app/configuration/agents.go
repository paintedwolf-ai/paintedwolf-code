package configuration

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/profiles"
)

type Agents struct {
	Registry     *orchestration.MemoryAgentRegistry
	ToolProfiles []sandbox.ToolProfile
	Postures     *profiles.PostureRegistry
}

func (b *Agents) Load(ctx context.Context) error {
	var err error
	b.Registry = orchestration.NewMemoryAgentRegistry()
	if err := orchestration.LoadRequiredAgentRegistry(ctx, b.Registry); err != nil {
		return fmt.Errorf("agent registry: %w", err)
	}
	if err := orchestration.ValidateGateAgents(b.Registry); err != nil {
		return fmt.Errorf("agent registry gates: %w", err)
	}
	b.ToolProfiles, err = sandbox.LoadToolProfiles()
	if err != nil {
		return fmt.Errorf("tool profiles: %w", err)
	}
	if err := orchestration.ValidateAgentToolProfiles(b.Registry, b.ToolProfiles); err != nil {
		return fmt.Errorf("agent tool profiles: %w", err)
	}
	b.Postures, err = profiles.LoadPostureRegistry()
	if err != nil {
		return fmt.Errorf("posture registry: %w", err)
	}
	return nil
}
