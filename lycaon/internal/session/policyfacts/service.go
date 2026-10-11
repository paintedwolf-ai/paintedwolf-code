package policyfacts

import (
	"context"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretmint"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type Profiles interface {
	PromptToolProfile(context.Context, *api.Session) (string, error)
}
type Service struct {
	profiles                   Profiles
	store                      Sessions
	workspace                  Workspace
	feedback                   Feedback
	loading                    ActiveTools
	surface                    TurnSurface
	Pipeline                   *oar.GuardPipeline
	MCP                        MCPRuntimeView
	doomLoop                   loopguard.DoomLoopGuard
	mintedCredentials          func() MintedCredentialSource
	rememberSecrets            secretmatch.RememberFunc
	secretFP                   *secretmatch.Fingerprinter
	harvestHas                 HarvestedFingerprint
	ignoredCredentialCandidate func(context.Context, string, string) bool
	credentialSlots            func(context.Context, *api.Session) *secretmint.Inspector
}

func New(profiles Profiles) *Service { return &Service{profiles: profiles} }
func (m *Service) FillSessionFacts(ctx context.Context, gc *oar.GuardContext, sess *api.Session, tool string, args map[string]any) {
	if gc == nil {
		return
	}
	if sess != nil {
		gc.Session.ProjectID = sess.ProjectID
		gc.Session.SessionID = sess.ID
		gc.Session.SessionPosture = string(sess.Posture)
		gc.Session.Principal = sess.OwnerPersonID
		gc.Session.WorkerLeg = sess.IsWorkerChild()
		if sess.ParentSessionID == "" {
			gc.Session.Profile = "coordinator"
		}
		gc.RegisterProvider("permission_profile", func(gc *oar.GuardContext) error {
			profile, err := m.profiles.PromptToolProfile(ctx, sess)
			if err != nil {
				return err
			}
			gc.Session.PermissionProfile = profile
			return nil
		})
	}
	if caller, ok := people.Caller(ctx); ok {
		gc.Session.Principal = caller.ID
		gc.Session.PrincipalRoles = []string{string(caller.Role)}
	}
	tools.RegisterRecoveryFacts(ctx, gc)
	gc.ObserveToolCall(tool, args)
	gc.DeriveToolClassFacts()
}

type Sessions interface {
	Get(context.Context, string) (*api.Session, error)
}
type Workspace interface {
	SettingsPath(context.Context, *api.Session) string
}
type Feedback interface {
	RenderResult(context.Context, string, *oar.PipelineResult) (*guidance.Refusal, bool, error)
	DeliverPostToolResult(context.Context, string, string, *oar.PipelineResult) (string, guidance.ToolResultFacts)
}
type ActiveTools interface{ Active(string) map[string]bool }
type TurnSurface interface{ PromptTurnSurfaceID(string) string }
type MCPRuntimeView interface {
	oar.MCPCatalogView
	ToolLoadingModes(context.Context, string) map[string]bool
}

func (m *Service) SetSources(sessions Sessions, workspace Workspace, feedback Feedback, loading ActiveTools) {
	m.store = sessions
	m.workspace = workspace
	m.feedback = feedback
	m.loading = loading
}
func (m *Service) SetSurface(surface TurnSurface)                 { m.surface = surface }
func (m *Service) SetPipeline(pipeline *oar.GuardPipeline)        { m.Pipeline = pipeline }
func (m *Service) SetMCPRuntime(runtime MCPRuntimeView)           { m.MCP = runtime }
func (m *Service) SetDoomLoopGuard(guard loopguard.DoomLoopGuard) { m.doomLoop = guard }
