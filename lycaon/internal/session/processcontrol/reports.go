package processcontrol

import "sync"

// maxProcessReports bounds retained completion reports for wait reconciliation.
const maxProcessReports = 256

// processReports preserves completion digests for waits settled by reconciliation.
type processReports struct {
	mu      sync.Mutex
	reports map[string]string
	order   []string
}

func processReportKey(sessionID, handle string) string { return sessionID + "\x00" + handle }

func (p *processReports) publish(sessionID, handle, report string) {
	key := processReportKey(sessionID, handle)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reports == nil {
		p.reports = map[string]string{}
	}
	if _, seen := p.reports[key]; !seen {
		p.order = append(p.order, key)
	}
	p.reports[key] = report
	for len(p.order) > maxProcessReports {
		delete(p.reports, p.order[0])
		p.order = p.order[1:]
	}
}

func (p *processReports) lookup(sessionID, handle string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	report, ok := p.reports[processReportKey(sessionID, handle)]
	return report, ok
}

// processReport reads completion digests for process and held-call handles.
func (m *Service) Report(sessionID, handle string) (string, bool) {
	if m == nil {
		return "", false
	}
	return m.processReports.lookup(sessionID, handle)
}
