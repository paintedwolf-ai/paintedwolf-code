package security

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type MCPProviders interface{ EnabledProviderIDs() []string }

func (b *Runtime) BuildAuthorization(moduleRoot string, profiles []sandbox.ToolProfile, perms *settings.ApprovalStore, gate func() hitl.ApprovalGate, metadata func() []tools.ToolMeta, access func(context.Context, *api.Session) sandbox.ToolAccess, budget func(string) spawn.WorkerToolBudget) error {
	audit, err := authzcontext.LoadAuditConfig(moduleRoot)
	if err != nil {
		return fmt.Errorf("audit config: %w", err)
	}
	b.Authority = authzcontext.NewSQLCapturer(b.database, audit, authzcontext.ProfileMap(profiles))
	if b.Authority == nil || b.Authority.Sealer == nil {
		return fmt.Errorf("authorization store required before session sealing")
	}
	sealer := b.Authority.Sealer
	sealer.Perms = perms
	sealer.ChatGrants = func(chat string) []hitl.ApprovalGrant {
		active := gate()
		if active == nil {
			return nil
		}
		return active.ListGrants(chat)
	}
	sealer.ToolAccess = access
	sealer.RegisteredTools = func() []string {
		metas := metadata()
		out := make([]string, 0, len(metas))
		for _, meta := range metas {
			out = append(out, meta.Name)
		}
		return out
	}
	sealer.SpawnAllowlist = func(ctx context.Context, sess *api.Session) []string {
		if b.spawnAgents != nil && sess != nil {
			if roster := b.spawnAgents(ctx, sess.ID); len(roster) > 0 {
				return roster
			}
		}
		return spawn.AmbientAllowedAgents()
	}
	sealer.WorkerToolBudget = budget
	return nil
}
func (b *Runtime) BindMCPInventory(providers MCPProviders) {
	b.Authority.Sealer.MCPInventory = func(context.Context) authzcontext.MCPInventory {
		return authzcontext.MCPInventory{ProviderIDs: providers.EnabledProviderIDs()}
	}
}
func (b *Runtime) BindSpawnAgents(source func(context.Context, string) []string) {
	b.spawnAgents = source
}
