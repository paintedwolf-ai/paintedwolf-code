package workerplan

import (
	"context"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// PlanReader loads Blueprint documents by project and path.
type PlanReader interface {
	Get(ctx context.Context, projectID, path string) (*api.Blueprint, error)
}

// ApprovalChecker reports whether a Blueprint revision is currently approved.
type ApprovalChecker interface {
	IsApproved(ctx context.Context, projectID, blueprintPath string) (bool, error)
}

// WorkflowReadyChecker reports whether implement handoff preconditions hold.
type WorkflowReadyChecker interface {
	ImplementWorkflowReady(ctx context.Context, sessionID, blueprintPath string) (bool, error)
}

// RunChecker enforces workflow run runnability.
type RunChecker interface {
	AssertRunnable(ctx context.Context, runID string) error
}

// ActiveRunReader looks up the active workflow run for a coordinator session.
// It returns nil when the session has none.
type ActiveRunReader interface {
	ActiveBySession(ctx context.Context, sessionID string) (*api.WorkflowRun, error)
}

// HeadSHAReader reads the workspace's HEAD commit.
type HeadSHAReader interface {
	HeadSHA(ctx context.Context, workspacePath string) (string, error)
}

// GitDiffReader checks working-tree and base-commit diffs for file criteria.
type GitDiffReader interface {
	DiffFromRef(ctx context.Context, projectDir, baseSHA, path string) (bool, error)
	HasUncommittedDiff(ctx context.Context, projectDir, path string) (bool, error)
}

// SourceRunReader lists the command and verify receipts of a worker session.
type SourceRunReader interface {
	WorkerSourceRuns(ctx context.Context, childSessionID string) ([]workercompletion.WorkerInvocationReceipt, error)
}

// JobStatusReader looks up worker jobs.
type JobStatusReader interface {
	Get(id string) (*api.WorkerTask, bool)
}

// SecurityGate checks the scan obligation of a session's latest landed change.
type SecurityGate interface {
	CheckSession(ctx context.Context, sessionID, hostDataDir string) error
}

// Deps are a plan service's collaborators. Every one is required: a gate
// with a missing collaborator would admit what it cannot check.
type Deps struct {
	Records       *workerrecords.Store
	Plans         PlanReader
	Approvals     ApprovalChecker
	WorkflowReady WorkflowReadyChecker
	Runs          RunChecker
	ActiveRuns    ActiveRunReader
	HeadSHA       HeadSHAReader
	GitDiff       GitDiffReader
	SourceRuns    SourceRunReader
	Jobs          JobStatusReader
	Security      SecurityGate
	HostDataDir   func(ctx context.Context, projectID string) (string, error)
}

// Service seeds planned workers from Blueprints, gates round admission,
// evaluates typed criteria and checks plan settlement before closeout.
type Service struct {
	records       *workerrecords.Store
	plans         PlanReader
	approvals     ApprovalChecker
	workflowReady WorkflowReadyChecker
	runs          RunChecker
	activeRuns    ActiveRunReader
	headSHA       HeadSHAReader
	gitDiff       GitDiffReader
	sourceRuns    SourceRunReader
	jobs          JobStatusReader
	security      SecurityGate
	hostDataDir   func(ctx context.Context, projectID string) (string, error)
}

// ErrIncomplete reports a plan service built without one of its collaborators.
var ErrIncomplete = errors.New("workerplan: missing dependency")

// New returns a plan service, or ErrIncomplete naming the first missing
// collaborator.
func New(d Deps) (*Service, error) {
	for _, dep := range []struct {
		name    string
		missing bool
	}{
		{"Records", d.Records == nil},
		{"Plans", d.Plans == nil},
		{"Approvals", d.Approvals == nil},
		{"WorkflowReady", d.WorkflowReady == nil},
		{"Runs", d.Runs == nil},
		{"ActiveRuns", d.ActiveRuns == nil},
		{"HeadSHA", d.HeadSHA == nil},
		{"GitDiff", d.GitDiff == nil},
		{"SourceRuns", d.SourceRuns == nil},
		{"Jobs", d.Jobs == nil},
		{"Security", d.Security == nil},
		{"HostDataDir", d.HostDataDir == nil},
	} {
		if dep.missing {
			return nil, fmt.Errorf("%w: %s", ErrIncomplete, dep.name)
		}
	}
	return &Service{
		records: d.Records, plans: d.Plans, approvals: d.Approvals, workflowReady: d.WorkflowReady,
		runs: d.Runs, activeRuns: d.ActiveRuns, headSHA: d.HeadSHA, gitDiff: d.GitDiff,
		sourceRuns: d.SourceRuns, jobs: d.Jobs, security: d.Security, hostDataDir: d.HostDataDir,
	}, nil
}

func coordinator(sessionID string) workerrecords.Actor {
	return workerrecords.Actor{SessionID: sessionID, Role: workerrecords.RoleCoordinator, PrincipalID: sessionID}
}
