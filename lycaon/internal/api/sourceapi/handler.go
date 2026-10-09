package sourceapi

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/api/gitadmin"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/fileops"
	"github.com/lycaon/lycaon/internal/project"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretspan"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/sourcecomparison"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Deps are the source routes' dependencies, fixed at construction.
type Deps struct {
	Git          *gitadmin.Handler
	MutationGate *project.MutationGate
	// CatalogSnapshot and WatchNeedsSeed replace the process source catalog
	// and platform watch policy; nil uses the process defaults.
	CatalogSnapshot CatalogSnapshotFunc
	WatchNeedsSeed  func(rootPath string) bool
	EditorClients   *editordoc.ClientLiveness
	EditorDocuments *editordoc.Service
	Events          events.ReplayHub
	FileBriefings   *filebriefing.Service
	// FileOperations journals file requests.
	FileOperations  *fileops.Service
	ManagedSecrets  *secretcap.Service
	ProjectRegistry project.Registry
	ScanCadence     *scancadence.Service
	SecretSpans     *secretspan.Screener
	SessionStore    session.Store
	// SourceInventory tracks workspace drift; nil leaves inventory to callers.
	SourceInventory InventoryService
	SourceLedger    *sourceledger.Store
	// SourceMutations applies source writes.
	SourceMutations *project.SourceMutationService
	VisualStore     visual.Store
	Workers         worker.WorkerQueue
	AttachmentStore func(context.Context, string) (blobstore.Store, bool)
	TryRunPromotion func(context.Context, string)
}

// Handler serves source views, file operations, and editor documents with one shared cache and watcher lifetime.
type Handler struct {
	Deps
	editorScreens   editorScreenMemo
	sourceIndexes   *project.SourceIndexCache
	sourceReaders   sourcecomparison.Cache
	sourceViews     sourceViewService
	sourceWatchJobs map[string]*sourceWatchJob
	// watchedProjects names the projects whose process-wide watch this host bound.
	watchedProjects map[string]struct{}
	sourceWatchMu   sync.Mutex
	warmupPolls     *warmupClock
	background      *taskgroup.Group
	responses       *httpio.Responder
	operations      Operations
}

type Operation struct{ ID, Method, Path string }
type Operations struct {
	CopyProjectSource        Operation
	CreateProjectSourceEntry Operation
	DeleteProjectSource      Operation
	RedoProjectSourceHistory Operation
	RenameProjectSource      Operation
	UndoProjectSourceHistory Operation
}

func New(responses *httpio.Responder, background *taskgroup.Group, operations Operations, deps Deps) Handler {
	httpio.RequireDependencies("sourceapi",
		httpio.Required{Name: "EditorDocuments", Present: deps.EditorDocuments != nil},
		httpio.Required{Name: "FileBriefings", Present: deps.FileBriefings != nil},
		httpio.Required{Name: "FileOperations", Present: deps.FileOperations != nil},
		httpio.Required{Name: "ManagedSecrets", Present: deps.ManagedSecrets != nil},
		httpio.Required{Name: "MutationGate", Present: deps.MutationGate != nil},
		httpio.Required{Name: "ProjectRegistry", Present: deps.ProjectRegistry != nil},
		httpio.Required{Name: "SessionStore", Present: deps.SessionStore != nil},
		httpio.Required{Name: "SourceLedger", Present: deps.SourceLedger != nil},
		httpio.Required{Name: "SourceMutations", Present: deps.SourceMutations != nil},
		httpio.Required{Name: "Workers", Present: deps.Workers != nil},
	)
	hub := deps.Events
	deps.FileOperations.Observe(func(request fileops.Request) { publishSourceRequest(hub, request) })
	// Editor saves apply through the same mutation service as other writes.
	deps.EditorDocuments.SetSourceMutations(deps.SourceMutations)
	return Handler{Deps: deps, responses: responses, background: background, operations: operations,
		sourceIndexes: project.NewSourceIndexCache(), sourceWatchJobs: make(map[string]*sourceWatchJob), watchedProjects: make(map[string]struct{}), warmupPolls: newWarmupClock()}
}

// SnapshotBytes reports retained current-source snapshot bytes across all views.
func (s *Handler) SnapshotBytes() int64 { return s.sourceViews.snapshotDisk.Used() }

func (s *Handler) EditorScreen(documentID, digest string) (*wire.SecretScreen, bool) {
	return s.editorScreens.get(documentID, digest)
}
