package scan

import "github.com/lycaon/lycaon/pkg/api"

// UnrecoveredScans retains unsuccessful work until a later completed execution
// of the same scanner covers its target. A delta cannot replace a full pass.
func UnrecoveredScans(scans []api.CodeScan) []api.CodeScan {
	var out []api.CodeScan
	for _, failed := range scans {
		if failed.Status == api.CodeScanStatusComplete {
			continue
		}
		recovered := false
		for _, later := range scans {
			if later.Status != api.CodeScanStatusComplete || later.ScannerID != failed.ScannerID || !later.CreatedAt.After(failed.CreatedAt) {
				continue
			}
			if later.TargetKind != api.ScanTargetPaths {
				recovered = true
				break
			}
			if failed.TargetKind != api.ScanTargetPaths || len(failed.TargetPaths) == 0 {
				continue
			}
			covered := true
			for _, path := range failed.TargetPaths {
				covered = covered && containsPath(later.TargetPaths, path)
			}
			if covered {
				recovered = true
				break
			}
		}
		if !recovered {
			out = append(out, failed)
		}
	}
	return out
}
