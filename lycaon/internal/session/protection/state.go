package protection

import (
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/pkg/api"
)

type Jobs interface {
	ActiveExecutionJobs(string) []bgprocess.ActiveExecutionJob
	ActiveDirectIPJobs(string) []bgprocess.ActiveDirectIPJob
}

// Service projects structured protection facts and deduplicates reconstruction events.
type Service struct {
	jobs                    Jobs
	directIPReconstructHook DirectIPReconstructHook
	reconstructedDirectIPMu sync.Mutex
	reconstructedDirectIP   map[string]map[string]struct{}
}

func New() *Service                  { return &Service{} }
func (m *Service) SetJobs(jobs Jobs) { m.jobs = jobs }

// DirectIPReconstructHook receives a reconstructed direct-IP fact.
type DirectIPReconstructHook func(sessionID, handle, toolCallID string)

// BuildProtectionState derives protection chrome from host facts.
func BuildProtectionState(sessionID string, reg Jobs) *api.SessionProtectionState {
	overrides := confine.CurrentEnvironmentOverrides()
	prot := &api.SessionProtectionState{
		ControlPlaneReadsAllowed: overrides.ControlPlaneReadsAllowed,
		AdditionalWriteRoots:     overrides.AdditionalWriteRoots,
	}
	if _, degraded := confine.EgressDegradedState(); degraded {
		prot.MediationUnavailable = true
	}
	if overrides.ApprovalsBypassed || overrides.SandboxDisabled {
		prot.SandboxBypass = true
	}
	if reg != nil {
		for _, job := range reg.ActiveExecutionJobs(sessionID) {
			prot.BackgroundExecution = append(prot.BackgroundExecution, api.SessionBackgroundExecutionJob{ProcessID: job.Handle, ToolCallID: job.ToolCallID, Capability: job.Capability, Status: api.SessionBackgroundDirectStatus(job.Status)})
		}
		for _, job := range reg.ActiveDirectIPJobs(sessionID) {
			status := api.SessionBackgroundDirectStatusActive
			if job.Status == bgprocess.JobLivenessUnknown {
				status = api.SessionBackgroundDirectStatusUnknown
			}
			prot.BackgroundDirect = append(prot.BackgroundDirect, api.SessionBackgroundDirectJob{
				ProcessID:  job.Handle,
				ToolCallID: job.ToolCallID,
				Status:     status,
			})
		}
	}
	if !prot.MediationUnavailable && !prot.SandboxBypass && !prot.ControlPlaneReadsAllowed && len(prot.AdditionalWriteRoots) == 0 && len(prot.BackgroundDirect) == 0 && len(prot.BackgroundExecution) == 0 {
		return nil
	}
	return prot
}

// SetDirectIPReconstructHook sets reconstructed direct-IP emission.
func (m *Service) SetDirectIPReconstructHook(fn DirectIPReconstructHook) {
	if m == nil {
		return
	}
	m.reconstructedDirectIPMu.Lock()
	m.directIPReconstructHook = fn
	m.reconstructedDirectIP = nil
	m.reconstructedDirectIPMu.Unlock()
}

// ProtectionStateForSession builds protection chrome for one session.
func (m *Service) ProtectionStateForSession(sessionID string) *api.SessionProtectionState {
	if m == nil {
		return BuildProtectionState(sessionID, nil)
	}
	prot := BuildProtectionState(sessionID, m.jobs)
	m.emitDirectIPReconstructed(sessionID, prot)
	return prot
}

func (m *Service) emitDirectIPReconstructed(sessionID string, prot *api.SessionProtectionState) {
	if m == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	type emission struct {
		handle     string
		toolCallID string
	}
	m.reconstructedDirectIPMu.Lock()
	hook := m.directIPReconstructHook
	if hook == nil {
		delete(m.reconstructedDirectIP, sessionID)
		m.reconstructedDirectIPMu.Unlock()
		return
	}
	seen := m.reconstructedDirectIP[sessionID]
	var jobs []api.SessionBackgroundDirectJob
	if prot != nil {
		jobs = prot.BackgroundDirect
	}
	active := make(map[string]struct{}, len(jobs))
	pending := make([]emission, 0, len(jobs))
	for _, job := range jobs {
		handle := strings.TrimSpace(job.ProcessID)
		if handle == "" {
			continue
		}
		active[handle] = struct{}{}
		if _, loaded := seen[handle]; loaded {
			continue
		}
		pending = append(pending, emission{handle: handle, toolCallID: strings.TrimSpace(job.ToolCallID)})
	}
	if m.reconstructedDirectIP == nil {
		m.reconstructedDirectIP = make(map[string]map[string]struct{})
	}
	if len(active) == 0 {
		delete(m.reconstructedDirectIP, sessionID)
	} else {
		m.reconstructedDirectIP[sessionID] = active
	}
	m.reconstructedDirectIPMu.Unlock()
	for _, item := range pending {
		hook(sessionID, item.handle, item.toolCallID)
	}
}

func (m *Service) Forget(sessionID string) {
	if m == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	m.reconstructedDirectIPMu.Lock()
	delete(m.reconstructedDirectIP, sessionID)
	m.reconstructedDirectIPMu.Unlock()
}

// ApplyProtection merges protection into session UI state.
func ApplyProtection(ui *api.SessionUiState, prot *api.SessionProtectionState) *api.SessionUiState {
	if prot == nil {
		return ui
	}
	if ui == nil {
		ui = &api.SessionUiState{}
	}
	ui.Protection = prot
	return ui
}
