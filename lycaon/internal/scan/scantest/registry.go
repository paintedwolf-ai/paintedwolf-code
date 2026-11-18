package scantest

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/scan/findings/findingstest"
	"sync"
	"sync/atomic"
	"time"

	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"

	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/pkg/api"
)

// MockScanner is a configurable scan.CodeScanner for tests.
type MockScanner struct {
	IDVal        string
	CategoryList []api.ScanCategory
	Result       *scanoutput.Result
	Err          error
	RunCalls     int
	mu           sync.Mutex
}

func (m *MockScanner) ID() string {
	if m.IDVal != "" {
		return m.IDVal
	}
	return "mock-scanner"
}

func (m *MockScanner) Categories() []api.ScanCategory {
	if len(m.CategoryList) > 0 {
		return m.CategoryList
	}
	return []api.ScanCategory{api.ScanCategorySecurity}
}

func (m *MockScanner) Run(ctx context.Context, req scan.ScanRequest) (*scanoutput.Result, error) {
	m.mu.Lock()
	m.RunCalls++
	m.mu.Unlock()
	if m.Err != nil {
		return nil, m.Err
	}
	if m.Result != nil {
		return m.Result, nil
	}
	return &scanoutput.Result{
		FindingsCount: 1,
		Categories:    req.Categories,
		Findings: []api.SecurityFinding{
			findingstest.Finding("mock:test", api.FindingLevelHigh, "mock finding", "", 0),
		},
	}, nil
}

// SlowMockScanner sleeps during Run to test runner concurrency.
type SlowMockScanner struct {
	IDVal   string
	Delay   time.Duration
	Running *int32
	Result  *scanoutput.Result
}

func (m *SlowMockScanner) ID() string {
	if m.IDVal != "" {
		return m.IDVal
	}
	return "slow-mock"
}

func (m *SlowMockScanner) Categories() []api.ScanCategory {
	return []api.ScanCategory{api.ScanCategorySecurity}
}

func (m *SlowMockScanner) Run(ctx context.Context, req scan.ScanRequest) (*scanoutput.Result, error) {
	if m.Running != nil {
		n := atomic.AddInt32(m.Running, 1)
		defer atomic.AddInt32(m.Running, -1)
		if n > 2 {
			panic("concurrency cap exceeded")
		}
	}
	if m.Delay > 0 {
		time.Sleep(m.Delay)
	}
	if m.Result != nil {
		return m.Result, nil
	}
	return &scanoutput.Result{FindingsCount: 1, Categories: req.Categories}, nil
}

// MockRegistry wraps a scanner for runner tests.
type MockRegistry struct {
	Scanner   scan.CodeScanner
	Scanners  []scan.CodeScanner
	Runtime   scancatalog.RuntimePolicy
	Driver    string
	Command   []string
	Contracts map[string]scancatalog.ScannerContract
}

func (r *MockRegistry) Register(_ scan.CodeScanner) error { return nil }

func (r *MockRegistry) Get(id string) (scan.CodeScanner, error) {
	for _, scanner := range r.allScanners() {
		if scanner.ID() == id {
			return scanner, nil
		}
	}
	return nil, fmt.Errorf("scanner %q not found", id)
}

func (r *MockRegistry) List(categories ...api.ScanCategory) []scan.ScannerMeta {
	var out []scan.ScannerMeta
	for _, scanner := range r.allScanners() {
		if len(categories) > 0 && !mockCategoryOverlap(scanner.Categories(), categories) {
			continue
		}
		driver := r.Driver
		if driver == "" {
			driver = "mock"
		}
		entry := scancatalog.ScannerEntry{
			ID: scanner.ID(), Driver: driver, Engine: "mock",
			ScopeKind: string(scancatalog.ScopeCustom), Categories: []string{string(api.ScanCategorySecurity)},
			Runtime: r.Runtime, Command: append([]string(nil), r.Command...),
		}
		contract := entry.Contract()
		if r.Contracts != nil {
			if override, ok := r.Contracts[scanner.ID()]; ok {
				contract = override
			}
		}
		out = append(out, scan.ScannerMeta{ID: scanner.ID(), Categories: scanner.Categories(), Contract: contract})
	}
	return out
}

func (r *MockRegistry) RunBest(ctx context.Context, categories []api.ScanCategory, req scan.ScanRequest) (*scanoutput.Result, error) {
	scanners := r.allScanners()
	if len(scanners) == 0 {
		return &scanoutput.Result{}, nil
	}
	return scanners[0].Run(ctx, req)
}

func (r *MockRegistry) allScanners() []scan.CodeScanner {
	if len(r.Scanners) > 0 {
		return r.Scanners
	}
	if r.Scanner != nil {
		return []scan.CodeScanner{r.Scanner}
	}
	return nil
}

func mockCategoryOverlap(have, want []api.ScanCategory) bool {
	for _, left := range have {
		for _, right := range want {
			if left == right {
				return true
			}
		}
	}
	return false
}
