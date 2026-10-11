package scanning

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/scan"
	scanregistry "github.com/lycaon/lycaon/internal/scan/registry"
)

// The host owns its default scanner generation; injected registries remain caller-owned.
func loadScannerRegistry(ctx context.Context, deps Dependencies, priority exec.ProcessPriority, appliesPath func(context.Context, string) bool) (scan.CodeScannerRegistry, error) {
	if deps.Fixtures.Registry != nil {
		return deps.Fixtures.Registry, nil
	}
	var key []byte
	if deps.FingerprintScannerKey != nil {
		key = deps.FingerprintScannerKey()
	}
	reg, err := scanregistry.New(ctx, scanregistry.Options{ScannerFingerprintKey: key, AdvisoryDatabase: deps.Fixtures.AdvisoryDatabase, ModuleRoot: deps.ModuleRoot, ProcessPriority: priority, ProjectTierApplies: appliesPath})
	if err != nil {
		return nil, fmt.Errorf("scan registry: %w", err)
	}
	if deps.Resources != nil {
		deps.Resources.Track("scanner-registry", 89, func(ctx context.Context) error { return reg.Close(ctx) })
	}
	return reg, nil
}
