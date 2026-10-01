package extpacks

import "context"

// ScannerRequirementChecker reports scanner availability for a scope.
type ScannerRequirementChecker interface {
	ScannerEnabled(ctx context.Context, projectID, scannerID string) bool
}
