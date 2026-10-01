package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/exec"
)

// ExpandCommand replaces allowlisted tokens in argv elements. reportPath fills
// {{report_path}} for scanners that write their report to a file instead of
// stdout; empty rejects argv that asks for the token.
func ExpandCommand(command []string, projectDir, scanTarget, reportPath string) ([]string, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("command is required")
	}
	out := make([]string, len(command))
	for i, arg := range command {
		expanded, err := expandCommandToken(arg, projectDir, scanTarget, reportPath)
		if err != nil {
			return nil, err
		}
		out[i] = expanded
	}
	return out, nil
}

func expandCommandToken(arg, projectDir, scanTarget, reportPath string) (string, error) {
	if !strings.Contains(arg, "{{") {
		return arg, nil
	}
	if strings.Count(arg, "{{") != strings.Count(arg, "}}") {
		return "", fmt.Errorf("unsupported template token in %q", arg)
	}
	replaced := arg
	if strings.Contains(replaced, ArgTokenProjectDir) {
		if strings.TrimSpace(projectDir) == "" {
			return "", fmt.Errorf("project_dir token requires project dir")
		}
		replaced = strings.ReplaceAll(replaced, ArgTokenProjectDir, projectDir)
	}
	if strings.Contains(replaced, ArgTokenScanTarget) {
		if strings.TrimSpace(scanTarget) == "" {
			return "", fmt.Errorf("scan_target token requires scan target")
		}
		replaced = strings.ReplaceAll(replaced, ArgTokenScanTarget, scanTarget)
	}
	if strings.Contains(replaced, ArgTokenReportPath) {
		if strings.TrimSpace(reportPath) == "" {
			return "", fmt.Errorf("report_path token requires a host report path")
		}
		replaced = strings.ReplaceAll(replaced, ArgTokenReportPath, reportPath)
	}
	if strings.Contains(replaced, "{{") {
		return "", fmt.Errorf("unsupported template token in %q", arg)
	}
	return replaced, nil
}

// BinaryOnPath reports whether command[0] resolves on the resolved PATH or as an absolute file.
func BinaryOnPath(command []string) bool {
	if len(command) == 0 {
		return false
	}
	binary := strings.TrimSpace(command[0])
	if binary == "" {
		return false
	}
	if filepath.IsAbs(binary) {
		_, err := os.Stat(binary)
		return err == nil
	}
	_, err := exec.LookPath(binary)
	return err == nil
}
