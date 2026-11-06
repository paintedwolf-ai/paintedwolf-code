package workspace

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/workerplacement"
)

// ErrWorkspaceStorageExhausted identifies a branch or seed write that failed
// because the central application-data volume cannot accept more data.
var ErrWorkspaceStorageExhausted = errors.New("worker workspace storage exhausted")

var queryAvailableStorageBytes = availableStorageBytes

func classifyProvisionError(err error) error {
	var capacityErr *CapacityError
	if err != nil && (isStorageExhausted(err) || errors.As(err, &capacityErr)) {
		return errors.Join(ErrWorkspaceStorageExhausted, err)
	}
	return err
}

// CapacityError reports insufficient bridge-cache capacity.
type CapacityError struct {
	Path      string
	Required  uint64
	Available uint64
}

func (e *CapacityError) Error() string {
	return fmt.Sprintf("worker workspace needs %d bytes but only %d bytes are available on %s", e.Required, e.Available, e.Path)
}

// ProvisionStrategy is the storage path selected from successful clone probes.
type ProvisionStrategy string

const (
	ProvisionDirectCoW  ProvisionStrategy = "direct_cow"
	ProvisionBridgeCoW  ProvisionStrategy = "bridge_cow"
	ProvisionDirectCopy ProvisionStrategy = "direct_copy"
)

// StageCostDeclared is reported before the first byte of a copy, with the cost in
// TotalEntries and TotalBytes.
const StageCostDeclared = "cost_declared"

// StageBranchPrepared closes one root's preparation with what it left out.
const StageBranchPrepared = "branch_prepared"

// PreparationProgress is ephemeral worker-claim progress. The totals are known
// only after a source survey has completed.
type PreparationProgress struct {
	Strategy          ProvisionStrategy
	Stage             string
	Files             int64
	Bytes             int64
	TotalBytes        int64
	TotalEntries      int64
	UnreadableEntries int64
	NotClonableReason workerplacement.NotClonableReason
}

type preparationReporterKey struct{}

const preparationReportInterval = 250 * time.Millisecond

// WithPreparationReporter attaches a worker-visible progress sink to a claim.
func WithPreparationReporter(ctx context.Context, report func(PreparationProgress)) context.Context {
	if report == nil {
		return ctx
	}
	var mu sync.Mutex
	var lastReport time.Time
	lastStage := ""
	throttled := func(progress PreparationProgress) {
		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		if progress.Stage == lastStage && now.Sub(lastReport) < preparationReportInterval {
			return
		}
		lastStage = progress.Stage
		lastReport = now
		report(progress)
	}
	return context.WithValue(ctx, preparationReporterKey{}, throttled)
}

func reportPreparation(ctx context.Context, progress PreparationProgress) {
	if report, ok := ctx.Value(preparationReporterKey{}).(func(PreparationProgress)); ok {
		report(progress)
	}
}

// Binding is one isolated worker sandbox — a copy of the project tree living
// outside the repo. There is no git interaction and no linked worktree.
type Binding struct {
	ID          string
	Root        string
	Preparation Preparation
}

// RootPreparation is what preparing one source root did, in counts and bytes.
type RootPreparation struct {
	Strategy          ProvisionStrategy
	Files             int64
	Directories       int64
	Symlinks          int64
	Bytes             int64
	ClonedFiles       int64
	ClonedBytes       int64
	CopiedBytes       int64
	UnreadableEntries int64
	// DeclaredEntries and DeclaredBytes are the cost stated before copying.
	DeclaredEntries int64
	DeclaredBytes   int64
}

// Preparation records the placement decision and each root's result. It is empty
// for a binding that reused an already complete branch.
type Preparation struct {
	Clonable          bool
	NotClonableReason workerplacement.NotClonableReason
	PlacementFolder   string
	Roots             []RootPreparation
}

// Manager creates and destroys per-job worker sandboxes, retaining a central
// bridge only when it enables copy-on-write branches.
type Manager struct {
	branchRoot string
	seedRoot   string
	// stateRoot holds the placement registry; branchRoot sits directly under it.
	stateRoot string
	locks     seedLockSet
}

// NewManager returns a workspace manager rooted at out-of-repo branch and seed bases.
func NewManager(branchRoot, seedRoot string) *Manager {
	branchRoot = strings.TrimSpace(branchRoot)
	stateRoot := filepath.Dir(branchRoot)
	if registry, err := workerplacement.Load(stateRoot); err == nil {
		workerplacement.Publish(registry)
	}
	return &Manager{
		branchRoot: branchRoot,
		seedRoot:   strings.TrimSpace(seedRoot),
		stateRoot:  stateRoot,
		locks:      newSeedLockSet(),
	}
}

func absDir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", fmt.Errorf("project dir required")
	}
	return filepath.Abs(dir)
}
