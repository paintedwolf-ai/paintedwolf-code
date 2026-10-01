package scan

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/pkg/api"
)

// MockScanner is a configurable CodeScanner for tests.
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

func (m *MockScanner) Run(ctx context.Context, req ScanRequest) (*scanoutput.Result, error) {
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
			scanfindings.FixtureFinding("mock:test", api.FindingLevelHigh, "mock finding", "", 0),
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

func (m *SlowMockScanner) Run(ctx context.Context, req ScanRequest) (*scanoutput.Result, error) {
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
	Scanner  CodeScanner
	Scanners []CodeScanner
	Runtime  scancatalog.RuntimePolicy
	Driver   string
	Command  []string
}

func (r *MockRegistry) Register(_ CodeScanner) error { return nil }

func (r *MockRegistry) Get(id string) (CodeScanner, error) {
	for _, scanner := range r.allScanners() {
		if scanner.ID() == id {
			return scanner, nil
		}
	}
	return nil, fmt.Errorf("scanner %q not found", id)
}

func (r *MockRegistry) List(categories ...api.ScanCategory) []ScannerMeta {
	var out []ScannerMeta
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
		out = append(out, ScannerMeta{ID: scanner.ID(), Categories: scanner.Categories(), Contract: entry.Contract()})
	}
	return out
}

func (r *MockRegistry) RunBest(ctx context.Context, categories []api.ScanCategory, req ScanRequest) (*scanoutput.Result, error) {
	scanners := r.allScanners()
	if len(scanners) == 0 {
		return &scanoutput.Result{}, nil
	}
	return scanners[0].Run(ctx, req)
}

func (r *MockRegistry) allScanners() []CodeScanner {
	if len(r.Scanners) > 0 {
		return r.Scanners
	}
	if r.Scanner != nil {
		return []CodeScanner{r.Scanner}
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
