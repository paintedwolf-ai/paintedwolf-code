package verification

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
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

// Service binds terminal verification evidence to the current source revision.
type Service struct {
	Evidence           *closeoutevidence.Service
	store              Store
	workspace          *sessionscope.Service
	transcript         *transcript.Service
	workerQueue        Workers
	workflows          Workflow
	evidenceStore      inspector.EvidenceStore
	verifyConfig       VerifyConfigResolver
	sourceLedger       sourceledger.Recorder
	verificationSource func(context.Context, string) (string, string)
	dataDir            string
}

func New(store Store, workspace *sessionscope.Service, transcript *transcript.Service) *Service {
	return &Service{store: store, workspace: workspace, transcript: transcript}
}
func (m *Service) SetDataDir(dir string)                        { m.dataDir = strings.TrimSpace(dir) }
func (m *Service) SetWorkers(workers Workers)                   { m.workerQueue = workers }
func (m *Service) SetWorkflow(workflow Workflow)                { m.workflows = workflow }
func (m *Service) SetSourceLedger(source sourceledger.Recorder) { m.sourceLedger = source }
func (m *Service) SetRevisionSource(source func(context.Context, string) (string, string)) {
	m.verificationSource = source
}
func (m *Service) ReadSourceRecords(ctx context.Context, sess *api.Session) ([]evidence.Record, error) {
	if m == nil || m.evidenceStore == nil || sess == nil {
		return nil, nil
	}
	runID := sessiontree.RootID(ctx, m.store, sess.ID)
	return m.evidenceStore.ReadAll(ctx, m.EvidenceRootFor(sess), runID, VerifySlot, evidence.GateTypeVerify)
}
func (m *Service) RequiresPhaseVerify(ctx context.Context, sessionID string) bool {
	if m == nil || m.workflows == nil {
		return false
	}
	return m.workflows.ActivePhaseRequiresEvidence(ctx, sessionID, "verify")
}
