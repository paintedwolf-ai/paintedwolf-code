package toolcontext

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/session/checkpointcontrol"
	"github.com/lycaon/lycaon/internal/session/execution"
	"github.com/lycaon/lycaon/internal/session/limits"
	"github.com/lycaon/lycaon/internal/session/profiles"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type Store interface {
	Get(context.Context, string) (*api.Session, error)
	UserTurnOrdinal(context.Context, string) (int, error)
}
type Projects interface {
	Get(context.Context, string) (*project.Project, error)
}

// Service binds invocation context to structured session and source facts.
type Service struct {
	store           Store
	workspace       *sessionscope.Service
	limits          *limits.Service
	profiles        *profiles.Service
	captures        *checkpointcontrol.Capture
	execution       *execution.Lifetime
	projects        Projects
	loopbackProv    tools.ContainerRecorder
	SourceLedger    sourceledger.Recorder
	sourceMutations sourceeffect.Journal
	editorDocuments tools.EditorDocuments
	credentialFiles tools.CredentialFiles
	repoProvider    repoinfo.Provider
	dataDir         string
}

func New(store Store, workspace *sessionscope.Service, limits *limits.Service, profiles *profiles.Service, captures *checkpointcontrol.Capture, execution *execution.Lifetime) *Service {
	return &Service{store: store, workspace: workspace, limits: limits, profiles: profiles, captures: captures, execution: execution}
}
func (m *Service) SetProjects(projects Projects) { m.projects = projects }
func (m *Service) SetDataDir(dir string)         { m.dataDir = strings.TrimSpace(dir) }
func (m *Service) SetContainers(containers tools.ContainerRecorder) {
	m.loopbackProv = containers
}
func (m *Service) SetSourceLedger(source sourceledger.Recorder)       { m.SourceLedger = source }
func (m *Service) SetSourceMutations(source sourceeffect.Journal)     { m.sourceMutations = source }
func (m *Service) SetEditorDocuments(documents tools.EditorDocuments) { m.editorDocuments = documents }
func (m *Service) SetCredentialFiles(files tools.CredentialFiles)     { m.credentialFiles = files }
func (m *Service) SetRepoProvider(provider repoinfo.Provider)         { m.repoProvider = provider }
