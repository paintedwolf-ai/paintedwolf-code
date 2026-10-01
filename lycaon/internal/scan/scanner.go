package scan

import (
	"context"
	"fmt"
	"strings"
	"time"

	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/pkg/api"
)

// ScanRequest is input for a code scan run.
type ScanRequest struct {
	ProjectDir string
	Categories []api.ScanCategory
	ScannerID  string
	Paths      []string
	// FileTimeout bounds the time an engine that reads files one at a time
	// spends on one of them; a file over it is reported as unscanned rather
	// than stalling the invocation. Zero leaves the engine's own limits.
	FileTimeout time.Duration
}

// ScannerMeta describes a registered scanner.
type ScannerMeta struct {
	ID         string
	Categories []api.ScanCategory
	Name       string
	Contract   scancatalog.ScannerContract
}

// CodeScanner runs one code analysis engine.
type CodeScanner interface {
	ID() string
	Categories() []api.ScanCategory
	Run(ctx context.Context, req ScanRequest) (*scanoutput.Result, error)
}

// CodeScannerRegistry registers and runs code scanners for host-initiated gate scans.
type CodeScannerRegistry interface {
	Register(s CodeScanner) error
	Get(id string) (CodeScanner, error)
	List(categories ...api.ScanCategory) []ScannerMeta
	RunBest(ctx context.Context, categories []api.ScanCategory, req ScanRequest) (*scanoutput.Result, error)
}

// ProjectScannerRegistry resolves project scanner selection.
type ProjectScannerRegistry interface {
	CodeScannerRegistry
	ListForProject(ctx context.Context, projectDir string, categories ...api.ScanCategory) []ScannerMeta
}

// Project-aware registries resolve project selection; other registries use device selection.
func ListSelectedScanners(ctx context.Context, registry CodeScannerRegistry, projectDir string, categories ...api.ScanCategory) []ScannerMeta {
	if projectRegistry, ok := registry.(ProjectScannerRegistry); ok {
		return projectRegistry.ListForProject(ctx, projectDir, categories...)
	}
	return registry.List(categories...)
}

// SelectedScannerContract resolves the selected scanner definition.
func SelectedScannerContract(ctx context.Context, registry CodeScannerRegistry, projectDir, scannerID string, categories ...api.ScanCategory) (scancatalog.ScannerContract, error) {
	id := strings.TrimSpace(scannerID)
	selected := ListSelectedScanners(ctx, registry, projectDir, categories...)
	if id == "" && len(selected) == 1 {
		id = selected[0].ID
	}
	if id == "" {
		return scancatalog.ScannerContract{}, fmt.Errorf("scanner contract: scanner id required for %d selected scanners", len(selected))
	}
	for _, meta := range selected {
		if meta.ID != id {
			continue
		}
		if !meta.Contract.Valid() {
			return scancatalog.ScannerContract{}, fmt.Errorf("scanner contract: %q has no valid contract", id)
		}
		if meta.Contract.ScannerID != meta.ID {
			return scancatalog.ScannerContract{}, fmt.Errorf("scanner contract: id %q does not match registry id %q", meta.Contract.ScannerID, meta.ID)
		}
		return meta.Contract, nil
	}
	return scancatalog.ScannerContract{}, fmt.Errorf("scanner contract: %q is not selected", id)
}
