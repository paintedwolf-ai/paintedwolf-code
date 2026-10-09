package sourceapi

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/api/briefingadmin"
	"github.com/lycaon/lycaon/internal/api/editoradmin"
	"github.com/lycaon/lycaon/internal/api/gitadmin"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/fileops"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretspan"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/sourcecomparison"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/worker"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
)

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
	SourceMutations *projectsource.SourceMutationService
	VisualStore     visual.Store
	Workers         worker.WorkerQueue
	AttachmentStore func(context.Context, string) (blobstore.Store, bool)
	TryRunPromotion func(context.Context, string)
}

type Handler struct {
	Editor          *editoradmin.Handler
	Analysis        *Analysis
	Briefings       *briefingadmin.Handler
	ComparisonViews *ComparisonViews
	Comparisons     *Comparisons
	History         *History
	Mutations       *Mutations
	Presentation    *Presentation
	Review          *Review
	Trees           *Trees
	Views           *Views
	Watch           *Watch
	Workspace       *Workspace
}

type Analysis struct {
	ProjectRegistry project.Registry
	SessionStore    session.Store
	Workspace       *Workspace
	responses       *httpio.Responder
	sourceIndexes   *projectsource.SourceIndexCache
	warmupPolls     *warmupClock
}

type ComparisonViews struct {
	Comparisons    *Comparisons
	ManagedSecrets *secretcap.Service
	SecretSpans    *secretspan.Screener
	Views          *Views
	Watch          *Watch
	background     *taskgroup.Group
	sourceReaders  *sourcecomparison.Cache
	sourceViews    *sourceViewService
}

type Comparisons struct {
	EditorDocuments *editordoc.Service
	Git             *gitadmin.Handler
	ManagedSecrets  *secretcap.Service
	ProjectRegistry project.Registry
	Review          *Review
	SecretSpans     *secretspan.Screener
	SessionStore    session.Store
	SourceLedger    *sourceledger.Store
	Views           *Views
	Workspace       *Workspace
	responses       *httpio.Responder
	sourceReaders   *sourcecomparison.Cache
}

type History struct {
	AttachmentStore func(context.Context, string) (blobstore.Store, bool)
	Comparisons     *Comparisons
	ProjectRegistry project.Registry
	Review          *Review
	SessionStore    session.Store
	SourceLedger    *sourceledger.Store
	VisualStore     visual.Store
	Watch           *Watch
	Workers         worker.WorkerQueue
	Workspace       *Workspace
	responses       *httpio.Responder
}

type Mutations struct {
	Comparisons     *Comparisons
	EditorDocuments *editordoc.Service
	FileOperations  *fileops.Service
	Git             *gitadmin.Handler
	MutationGate    *project.MutationGate
	ProjectRegistry project.Registry
	SessionStore    session.Store
	SourceLedger    *sourceledger.Store
	SourceMutations *projectsource.SourceMutationService
	Workspace       *Workspace
	background      *taskgroup.Group
	operations      Operations
	responses       *httpio.Responder
}

type Presentation struct {
	Views     *Views
	responses *httpio.Responder
}

type Review struct {
	Comparisons     *Comparisons
	Git             *gitadmin.Handler
	ProjectRegistry project.Registry
	SessionStore    session.Store
	SourceLedger    *sourceledger.Store
	responses       *httpio.Responder
}

type Trees struct {
	Presentation    *Presentation
	ProjectRegistry project.Registry
	Review          *Review
	SourceLedger    *sourceledger.Store
	Views           *Views
	Watch           *Watch
	background      *taskgroup.Group
	responses       *httpio.Responder
	sourceViews     *sourceViewService
}

type Views struct {
	ComparisonViews *ComparisonViews
	Comparisons     *Comparisons
	Events          events.ReplayHub
	ProjectRegistry project.Registry
	SessionStore    session.Store
	Trees           *Trees
	Workspace       *Workspace
	background      *taskgroup.Group
	responses       *httpio.Responder
	sourceReaders   *sourcecomparison.Cache
	sourceViews     *sourceViewService
}

type Watch struct {
	CatalogSnapshot CatalogSnapshotFunc
	EditorDocuments *editordoc.Service
	ProjectRegistry project.Registry
	ScanCadence     *scancadence.Service
	SourceInventory InventoryService
	WatchNeedsSeed  func(rootPath string) bool
	background      *taskgroup.Group
	responses       *httpio.Responder
	sourceWatchJobs map[string]*sourceWatchJob
	sourceWatchMu   sync.Mutex
}

type Workspace struct {
	ManagedSecrets  *secretcap.Service
	MutationGate    *project.MutationGate
	ProjectRegistry project.Registry
	SecretSpans     *secretspan.Screener
	SessionStore    session.Store
	SourceLedger    *sourceledger.Store
	Watch           *Watch
	Workers         worker.WorkerQueue
	responses       *httpio.Responder
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

	editor := editoradmin.New(responses, background, editoradmin.Dependencies{EditorClients: deps.EditorClients, EditorDocuments: deps.EditorDocuments, Events: deps.Events, ManagedSecrets: deps.ManagedSecrets, MutationGate: deps.MutationGate, ProjectRegistry: deps.ProjectRegistry, SecretSpans: deps.SecretSpans, SessionStore: deps.SessionStore, TryRunPromotion: deps.TryRunPromotion})
	sourceViews := &sourceViewService{}
	sourceReaders := &sourcecomparison.Cache{}
	warmupPolls := newWarmupClock()
	h := Handler{Editor: editor}
	h.Analysis = &Analysis{ProjectRegistry: deps.ProjectRegistry, SessionStore: deps.SessionStore, responses: responses, sourceIndexes: projectsource.NewSourceIndexCache(), warmupPolls: warmupPolls}
	h.ComparisonViews = &ComparisonViews{ManagedSecrets: deps.ManagedSecrets, SecretSpans: deps.SecretSpans, background: background, sourceReaders: sourceReaders, sourceViews: sourceViews}
	h.Comparisons = &Comparisons{EditorDocuments: deps.EditorDocuments, Git: deps.Git, ManagedSecrets: deps.ManagedSecrets, ProjectRegistry: deps.ProjectRegistry, SecretSpans: deps.SecretSpans, SessionStore: deps.SessionStore, SourceLedger: deps.SourceLedger, responses: responses, sourceReaders: sourceReaders}
	h.History = &History{AttachmentStore: deps.AttachmentStore, ProjectRegistry: deps.ProjectRegistry, SessionStore: deps.SessionStore, SourceLedger: deps.SourceLedger, VisualStore: deps.VisualStore, Workers: deps.Workers, responses: responses}
	h.Mutations = &Mutations{EditorDocuments: deps.EditorDocuments, FileOperations: deps.FileOperations, Git: deps.Git, MutationGate: deps.MutationGate, ProjectRegistry: deps.ProjectRegistry, SessionStore: deps.SessionStore, SourceLedger: deps.SourceLedger, SourceMutations: deps.SourceMutations, background: background, operations: operations, responses: responses}
	h.Presentation = &Presentation{responses: responses}
	h.Review = &Review{Git: deps.Git, ProjectRegistry: deps.ProjectRegistry, SessionStore: deps.SessionStore, SourceLedger: deps.SourceLedger, responses: responses}
	h.Trees = &Trees{ProjectRegistry: deps.ProjectRegistry, SourceLedger: deps.SourceLedger, background: background, responses: responses, sourceViews: sourceViews}
	h.Views = &Views{Events: deps.Events, ProjectRegistry: deps.ProjectRegistry, SessionStore: deps.SessionStore, background: background, responses: responses, sourceReaders: sourceReaders, sourceViews: sourceViews}
	h.Watch = &Watch{CatalogSnapshot: deps.CatalogSnapshot, EditorDocuments: deps.EditorDocuments, ProjectRegistry: deps.ProjectRegistry, ScanCadence: deps.ScanCadence, SourceInventory: deps.SourceInventory, WatchNeedsSeed: deps.WatchNeedsSeed, background: background, responses: responses, sourceWatchJobs: make(map[string]*sourceWatchJob)}
	h.Workspace = &Workspace{ManagedSecrets: deps.ManagedSecrets, MutationGate: deps.MutationGate, ProjectRegistry: deps.ProjectRegistry, SecretSpans: deps.SecretSpans, SessionStore: deps.SessionStore, SourceLedger: deps.SourceLedger, Workers: deps.Workers, responses: responses}
	h.Analysis.Workspace = h.Workspace
	h.ComparisonViews.Comparisons = h.Comparisons
	h.ComparisonViews.Views = h.Views
	h.ComparisonViews.Watch = h.Watch
	h.Comparisons.Review = h.Review
	h.Comparisons.Views = h.Views
	h.Comparisons.Workspace = h.Workspace
	h.History.Comparisons = h.Comparisons
	h.History.Review = h.Review
	h.History.Watch = h.Watch
	h.History.Workspace = h.Workspace
	h.Mutations.Comparisons = h.Comparisons
	h.Mutations.Workspace = h.Workspace
	h.Presentation.Views = h.Views
	h.Review.Comparisons = h.Comparisons
	h.Trees.Presentation = h.Presentation
	h.Trees.Review = h.Review
	h.Trees.Views = h.Views
	h.Trees.Watch = h.Watch
	h.Views.ComparisonViews = h.ComparisonViews
	h.Views.Comparisons = h.Comparisons
	h.Views.Trees = h.Trees
	h.Views.Workspace = h.Workspace
	h.Workspace.Watch = h.Watch
	h.Briefings = briefingadmin.New(responses, briefingadmin.Dependencies{EditorDocuments: deps.EditorDocuments, FileBriefings: deps.FileBriefings, ProjectRegistry: deps.ProjectRegistry, SessionStore: deps.SessionStore, SourceLedger: deps.SourceLedger, WorkerBranchRoot: h.Workspace.WorkerBranchRoot})
	return h
}

func (s *Views) SnapshotBytes() int64 { return s.sourceViews.snapshotDisk.Used() }
