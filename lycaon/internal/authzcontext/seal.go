package authzcontext

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

// ErrNilStore indicates Seal was called without a backing store.
var ErrNilStore = errors.New("authzcontext: nil store")

// ContextStore persists append-only authorization context chains.
type ContextStore interface {
	AppendContext(ctx context.Context, c Context) (appended bool, err error)
	LatestContext(ctx context.Context, sessionID string) (*Context, error)
	ListContexts(ctx context.Context, sessionID string) ([]Context, error)
}

// Sealer writes tamper-evident authorization context rows at run-start.
type Sealer struct {
	Store            ContextStore
	Profiles         map[string]sandbox.ToolProfile
	Perms            *settings.ApprovalStore
	ChatGrants       func(chatSessionID string) []hitl.ApprovalGrant
	MCPInventory     func(ctx context.Context) MCPInventory
	ToolAccess       func(ctx context.Context, sess *api.Session) sandbox.ToolAccess
	RegisteredTools  func() []string
	SpawnAllowlist   func(ctx context.Context, sess *api.Session) []string
	WorkerToolBudget func(projectDir string) spawn.WorkerToolBudget
}

// Seal assembles and appends the effective grant set when config_hash drifted.
// An unchanged config_hash succeeds without appending a row.
func (s *Sealer) Seal(ctx context.Context, sess *api.Session, profileID, workerJobID string) error {
	if s == nil || s.Store == nil {
		return authzledger.ErrSealFailed
	}
	if sess == nil || strings.TrimSpace(sess.ID) == "" {
		return authzledger.ErrSealFailed
	}
	profileID = strings.TrimSpace(profileID)
	var profile sandbox.ToolProfile
	if s.Profiles != nil {
		profile = s.Profiles[profileID]
	}
	profile = s.effectiveProfile(ctx, sess, profile)
	perms := ApprovalConfigForSession(s.Perms, sess)
	mcpInv := s.mcpInventory(ctx)
	mcpInv.Tools = MCPToolsFromProfile(profile)
	in := AssembleInput{
		Session:          sess,
		ProfileID:        profileID,
		Profile:          profile,
		Perms:            perms,
		ChatGrants:       s.chatGrants(sess.ID),
		WorkerJobID:      workerJobID,
		MCPInventory:     mcpInv,
		SpawnAllowlist:   s.spawnAllowlist(ctx, sess),
		WorkerToolBudget: s.workerBudget(sess),
	}
	c := Assemble(in)
	latest, err := s.Store.LatestContext(ctx, sess.ID)
	if err != nil {
		return fmt.Errorf("%w: latest: %w", authzledger.ErrSealFailed, err)
	}
	if latest != nil && latest.ConfigHash == c.ConfigHash {
		return nil
	}
	_, err = s.Store.AppendContext(ctx, c)
	if err != nil {
		return fmt.Errorf("%w: append: %w", authzledger.ErrSealFailed, err)
	}
	return nil
}

func (s *Sealer) chatGrants(chatSessionID string) []hitl.ApprovalGrant {
	if s == nil || s.ChatGrants == nil {
		return nil
	}
	all := s.ChatGrants(chatSessionID)
	out := make([]hitl.ApprovalGrant, 0, len(all))
	for _, grant := range all {
		if grant.Scope == hitl.ApprovalGrantScopeChat {
			out = append(out, grant)
		}
	}
	return out
}

func (s *Sealer) effectiveProfile(ctx context.Context, sess *api.Session, profile sandbox.ToolProfile) sandbox.ToolProfile {
	if s == nil || s.ToolAccess == nil || s.ToolAccess(ctx, sess) != sandbox.ToolAccessAll || s.RegisteredTools == nil {
		return profile
	}
	profile.Tools = make(map[string]bool)
	for _, name := range s.RegisteredTools() {
		name = strings.TrimSpace(name)
		if name != "" && !profile.ToolDenied(name) {
			profile.Tools[name] = true
		}
	}
	return profile
}

func (s *Sealer) mcpInventory(ctx context.Context) MCPInventory {
	if s == nil || s.MCPInventory == nil {
		return MCPInventory{}
	}
	return s.MCPInventory(ctx)
}

func (s *Sealer) spawnAllowlist(ctx context.Context, sess *api.Session) []string {
	if s == nil || s.SpawnAllowlist == nil {
		return spawn.AmbientAllowedAgents()
	}
	return s.SpawnAllowlist(ctx, sess)
}

func (s *Sealer) workerBudget(sess *api.Session) spawn.WorkerToolBudget {
	if s == nil || s.WorkerToolBudget == nil || sess == nil {
		return spawn.DefaultWorkerToolBudget()
	}
	return s.WorkerToolBudget(strings.TrimSpace(sess.WorkspacePath))
}
