package verification

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/session/closeoutevidence"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/transcript"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/pkg/api"
)

type Store interface {
	Get(context.Context, string) (*api.Session, error)
	GetMessages(context.Context, string) ([]api.Message, error)
}
type Workers interface {
	Get(string) (*api.WorkerTask, bool)
}
type Workflow interface {
	ActivePhaseRequiresEvidence(ctx context.Context, sessionID, evidenceType string) bool
}

// WorkspaceTrackerProvider resolves worker-workspace-scoped observation trackers and check bindings.
type WorkspaceTrackerProvider interface {
	TrackerForSession(jobID, sessionID, workspaceRoot, workspacePath string) (*observation.Tracker, string)
	BindingForCheck(jobID, sessionID, workspaceRoot, checkID string) (observation.VerificationBinding, bool)
	BindVerificationReceipt(jobID, childSessionID, workspacePath, branchRoot, checkID, checkpointID string, scopeRoots []string) (observation.VerificationBinding, bool)
}

// ObservationBindings manages session- and workspace-scoped observation trackers and check bindings.
type ObservationBindings struct {
	ServiceConfigOps
	svc               *Service
	workspaceTrackers WorkspaceTrackerProvider
	obsMu             sync.RWMutex
	obsTrackers       map[string]*observation.Tracker
	obsBindings       map[string]observation.VerificationBinding
	obsCheckpoints    map[string]string
}

// ServiceConfigOps provides dependency configuration setters on Service.
type ServiceConfigOps struct{ svc *Service }

// Service binds terminal verification evidence to the current source revision.
type Service struct {
	ObservationBindings
	Evidence           *closeoutevidence.Service
	store              Store
	workspace          *sessionscope.Service
	transcript         *transcript.Service
	workerQueue        Workers
	workflows          Workflow
	evidenceStore      inspector.EvidenceStore
	verifyConfig       VerifyConfigResolver
	sourceObservations *sourceledger.Inventory
	verificationSource func(context.Context, string) (string, string)
	dataDir            string
}

func New(store Store, workspace *sessionscope.Service, transcript *transcript.Service) *Service {
	s := &Service{store: store, workspace: workspace, transcript: transcript}
	s.ObservationBindings.svc = s
	s.ObservationBindings.ServiceConfigOps.svc = s
	return s
}

func (c *ServiceConfigOps) SetDataDir(dir string)      { c.svc.dataDir = strings.TrimSpace(dir) }
func (c *ServiceConfigOps) SetWorkers(workers Workers) { c.svc.workerQueue = workers }
func (c *ServiceConfigOps) SetWorkflow(workflow Workflow) {
	c.svc.workflows = workflow
}
func (c *ServiceConfigOps) SetSourceObservations(source *sourceledger.Inventory) {
	c.svc.sourceObservations = source
}
func (c *ServiceConfigOps) SetRevisionSource(source func(context.Context, string) (string, string)) {
	c.svc.verificationSource = source
}
func (c *ServiceConfigOps) SetWorkspaceTrackers(p WorkspaceTrackerProvider) {
	c.svc.workspaceTrackers = p
}
func (c *ServiceConfigOps) SetEvidenceStore(s inspector.EvidenceStore) {
	if c != nil && c.svc != nil {
		c.svc.evidenceStore = s
	}
}
func (c *ServiceConfigOps) SetVerifyConfig(r VerifyConfigResolver) {
	if c != nil && c.svc != nil {
		c.svc.verifyConfig = r
	}
}

func (c *ServiceConfigOps) ReadSourceRecords(ctx context.Context, sess *api.Session) ([]evidence.Record, error) {
	if c == nil || c.svc == nil || c.svc.evidenceStore == nil || sess == nil {
		return nil, nil
	}
	runID := sessiontree.RootID(ctx, c.svc.store, sess.ID)
	return c.svc.evidenceStore.ReadAll(ctx, c.svc.EvidenceRootFor(sess), runID, VerifySlot, evidence.GateTypeVerify)
}
func (c *ServiceConfigOps) RequiresPhaseVerify(ctx context.Context, sessionID string) bool {
	if c == nil || c.svc == nil || c.svc.workflows == nil {
		return false
	}
	return c.svc.workflows.ActivePhaseRequiresEvidence(ctx, sessionID, "verify")
}
func (c *ServiceConfigOps) SourceVerifyCommand(ctx context.Context, projectDir string) string {
	if c == nil || c.svc == nil || c.svc.verifyConfig == nil {
		return ""
	}
	return strings.TrimSpace(c.svc.verifyConfig.VerifyTestCommand(projectDir))
}

