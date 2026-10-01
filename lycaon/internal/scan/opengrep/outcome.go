package opengrep

import "fmt"

// ValidateReportExit accepts complete or diagnosed partial reports.
func ValidateReportExit(code, findings, diagnostics int) error {
	switch code {
	case 0:
		return nil
	case 1:
		if findings > 0 {
			return nil
		}
	case 2, 3, 4, 5:
		if diagnostics > 0 {
			return nil
		}
	}
	return fmt.Errorf("opengrep exit %d does not establish a successful or diagnosed partial scan", code)
}
