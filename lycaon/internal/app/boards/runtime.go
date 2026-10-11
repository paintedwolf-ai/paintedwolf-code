package boards

import (
	"context"

	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/call"
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/internal/worker"
)

// ResourceTracker registers closers for lifecycle management.
type ResourceTracker interface {
	Track(name string, priority int, closeFn func(context.Context) error)
}

// Runtime holds the board snapshot, findings, progress, visual, and research runtimes.
type Runtime struct {
	Snapshot       *board.SnapshotBuilder
	Findings       *findings.SQLStore
	Progress       *progress.SQLStore
	Visual         visual.Store
	Calls          *call.SQLManager
	Grounding      *toolhost.GroundingService
	ParentWaiter   *worker.ParentWorkerWaiter
	RepoProvider   repoinfo.Provider
	BudgetLedger   *worker.SQLBudgetLedger
	AnswerDecision *worker.AnswerDecisionService
	BrowserPool    *browser.Pool
	BrowserRaster  *browser.Rasterizer
	WebWarmer      *webresearch.Warmer
	WarmRunner     *webresearch.WarmRunner
	WebCreds       *webresearch.CredentialStore
	WebRuntime     webresearch.Runtime
	WebDiscoverer  webresearch.DirectDiscovererFactory
	deps           Dependencies
}
