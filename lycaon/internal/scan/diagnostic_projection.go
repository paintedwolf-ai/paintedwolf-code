package scan

import (
	"strings"

	"github.com/lycaon/lycaon/internal/observability"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/pkg/api"
)

// DiagnosticStream identifies report or console output.
type DiagnosticStream string

const (
	DiagnosticReport  DiagnosticStream = "report"
	DiagnosticConsole DiagnosticStream = "console"
)

// diagnosticExcerptBytes bounds an excerpt after screening.
const diagnosticExcerptBytes = 1024

// diagnosticWithheldMarker replaces secret-scanner output.
const diagnosticWithheldMarker = "[output withheld: secret scanner findings]"

// DiagnosticSubject provides scanner disclosure facts.
type DiagnosticSubject struct {
	Stream     DiagnosticStream
	ScopeKind  scancatalog.ScopeKind
	Categories []api.ScanCategory
}

// secretBearing reports whether output may contain credentials.
func (d DiagnosticSubject) secretBearing() bool {
	if d.ScopeKind == scancatalog.ScopeSecrets {
		return true
	}
	for _, category := range d.Categories {
		if category == api.ScanCategorySecret {
			return true
		}
	}
	return false
}

// DiagnosticExcerpt screens and bounds scanner output for errors.
func DiagnosticExcerpt(subject DiagnosticSubject, raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return ""
	}
	if subject.secretBearing() {
		return diagnosticWithheldMarker
	}
	text = strings.TrimSpace(observability.RedactCaptureText(text))
	if len(text) <= diagnosticExcerptBytes {
		return text
	}
	// Reports keep the head; console output keeps the tail.
	if subject.Stream == DiagnosticReport {
		return text[:diagnosticExcerptBytes]
	}
	return text[len(text)-diagnosticExcerptBytes:]
}

// DiagnosticSuffix formats a labelled error excerpt.
func DiagnosticSuffix(label string, subject DiagnosticSubject, raw []byte) string {
	excerpt := DiagnosticExcerpt(subject, raw)
	if excerpt == "" {
		return ""
	}
	return "; " + label + ": " + excerpt
}
