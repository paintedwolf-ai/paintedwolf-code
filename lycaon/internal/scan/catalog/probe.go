package catalog

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
)

// ProbeTimeout bounds `--version`-class install checks.
const ProbeTimeout = 10 * time.Second

// ProbeResult is one install-check row: did this scanner's binary actually run here.
type ProbeResult struct {
	ScannerID string `json:"scanner_id"`
	OK        bool   `json:"ok"`
	// Detail is the tool's own version line on success, or the failure reason.
	Detail string `json:"detail,omitempty"`
	// BinaryFound is true when probe argv[0] is on PATH.
	BinaryFound bool `json:"binary_found"`
}

// ProbeScanner runs the catalog probe command and reports whether the binary ran.
func ProbeScanner(ctx context.Context, scannerID string, probe []string) ProbeResult {
	res := ProbeResult{ScannerID: scannerID}
	if len(probe) == 0 {
		res.Detail = "no probe command for this scanner"
		return res
	}
	res.BinaryFound = BinaryOnPath(probe)
	if !res.BinaryFound {
		res.Detail = fmt.Sprintf("%s is not on PATH", probe[0])
		return res
	}

	stdout, stderr, exitCode, err := exec.RunSeparate(ctx, probe[0], probe[1:], exec.ExecOpts{
		Launch:          exec.ExternalScannerLaunch(scannerID),
		Timeout:         ProbeTimeout,
		MaxOutputBytes:  exec.DefaultMaxOutputBytes,
		ProcessPriority: exec.ProcessPriorityBelowNormal,
	})
	if err != nil && exitCode < 0 {
		res.Detail = err.Error()
		return res
	}
	// Version banners land on either stream depending on the tool.
	detail := firstLine(stdout)
	if detail == "" {
		detail = firstLine(stderr)
	}
	if exitCode != 0 {
		if detail == "" {
			detail = fmt.Sprintf("probe exited %d", exitCode)
		}
		res.Detail = detail
		return res
	}
	res.OK = true
	res.Detail = detail
	return res
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return ""
	}
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}
