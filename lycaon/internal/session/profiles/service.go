package profiles

import (
	"context"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/session/catalog"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/pkg/api"
)

type SessionReader interface {
	Get(context.Context, string) (*api.Session, error)
}
type Projects interface {
	Get(context.Context, string) (*project.Project, error)
}

// Service compiles profile-specific skills and observed machine resources.
type Service struct {
	workflowToolAccess    WorkflowToolAccessView
	postures              *PostureRegistry
	postureOverlay        scopedstore.LRU[*PostureRegistry]
	trustSurfaces         *settings.TrustSurfacesStore
	store                 SessionReader
	catalog               *catalog.Service
	workspace             *sessionscope.Service
	projects              Projects
	Agents                AgentProfileResolver
	skillsGate            *settings.ProjectSurfaceGate
	skillsCache           skills.ProjectCache
	hostResources         *hostresources.Service
	approvals             *settings.ApprovalStore
	postureObserver       func(context.Context, string, gate.Posture)
	externalObserver      func(context.Context, string, gate.Posture, bool)
	commandExternalHidden func() bool
	coordinatorProfile    func(context.Context, string) string
}

func New(store SessionReader, catalog *catalog.Service, workspace *sessionscope.Service) *Service {
	return &Service{store: store, catalog: catalog, workspace: workspace}
}
func (m *Service) SetProjects(projects Projects)                { m.projects = projects }
func (m *Service) SetAgentRegistry(agents AgentProfileResolver) { m.Agents = agents }
func (m *Service) SetApprovals(approvals *settings.ApprovalStore) {
	m.approvals = approvals
}
func (m *Service) SetPostureObserver(observer func(context.Context, string, gate.Posture)) {
	m.postureObserver = observer
}
func (m *Service) SetExternalObserver(observer func(context.Context, string, gate.Posture, bool)) {
	m.externalObserver = observer
}
func (m *Service) SetCommandExternalHidden(fn func() bool) {
	m.commandExternalHidden = fn
}
func (m *Service) SetCoordinatorProfile(resolve func(context.Context, string) string) {
	m.coordinatorProfile = resolve
}

// SetPostureRegistry wires posture → tool profile resolution.
func (m *Service) SetPostureRegistry(r *PostureRegistry) {
	m.postures = r
}

func (m *Service) SetTrustSurfaces(surfaces *settings.TrustSurfacesStore) { m.trustSurfaces = surfaces }
