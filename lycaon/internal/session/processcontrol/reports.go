package processcontrol

import (
	"sync"

	"github.com/lycaon/lycaon/internal/bgprocess"
)

// maxProcessReports bounds retained completion reports for wait reconciliation.
const maxProcessReports = 256

type processReport struct {
	digest string
	exit   *bgprocess.ExitObservation
}

// processReports preserves completion digests for waits settled by reconciliation.
type processReports struct {
	mu          sync.Mutex
	reports     map[string]processReport
	settledTemp map[string]bool
	order       []string
}

func processReportKey(sessionID, handle string) string { return sessionID + "\x00" + handle }

// claimSharedTemp ensures background shared-temp settlement runs at most once per job handle.
func (p *processReports) claimSharedTemp(sessionID, handle string) bool {
	if handle == "" {
		return true
	}
	key := processReportKey(sessionID, handle)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.settledTemp == nil {
		p.settledTemp = map[string]bool{}
	}
	if p.settledTemp[key] {
		return false
	}
	p.settledTemp[key] = true
	return true
}

func (p *processReports) publish(sessionID, handle string, report processReport) {
	key := processReportKey(sessionID, handle)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reports == nil {
		p.reports = map[string]processReport{}
	}
	if _, seen := p.reports[key]; !seen {
		p.order = append(p.order, key)
	}
	p.reports[key] = report
	for len(p.order) > maxProcessReports {
		evicted := p.order[0]
		delete(p.reports, evicted)
		delete(p.settledTemp, evicted)
		p.order = p.order[1:]
	}
}

func (p *processReports) lookup(sessionID, handle string) (processReport, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	report, ok := p.reports[processReportKey(sessionID, handle)]
	return report, ok
}

// Report reads completion digests for process and held-call handles.
func (m *Service) Report(sessionID, handle string) (string, bool) {
	if m == nil {
		return "", false
	}
	report, ok := m.processReports.lookup(sessionID, handle)
	return report.digest, ok
}
