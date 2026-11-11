package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
)

func TestAuditDeclarationReferencesPassesShippedDeclarations(t *testing.T) {
	var report bytes.Buffer
	code := auditDeclarationReferences(&report, oar.AuditDeclarationReferences)
	if code != 0 {
		t.Fatalf("declaration reference audit failed on the shipped declarations (exit %d):\n%s", code, report.String())
	}
	if report.Len() != 0 {
		t.Fatalf("unexpected audit output:\n%s", report.String())
	}
}

func TestAuditDeclarationReferencesReportsUnreferencedFacts(t *testing.T) {
	var report bytes.Buffer
	code := auditDeclarationReferences(&report, func() ([]string, error) {
		return []string{"paintedwolf.example", "paintedwolf.symbol"}, nil
	})
	if code != 1 {
		t.Fatalf("unreferenced facts exit code = %d, want 1", code)
	}
	for _, name := range []string{`"paintedwolf.example"`, `"paintedwolf.symbol"`} {
		if !strings.Contains(report.String(), name) {
			t.Fatalf("audit output missing %s:\n%s", name, report.String())
		}
	}
}

func TestAuditDeclarationReferencesReportsAuditErrors(t *testing.T) {
	var report bytes.Buffer
	code := auditDeclarationReferences(&report, func() ([]string, error) {
		return nil, errors.New("corpus unreadable")
	})
	if code != 2 {
		t.Fatalf("audit error exit code = %d, want 2", code)
	}
	if !strings.Contains(report.String(), "corpus unreadable") {
		t.Fatalf("audit output missing the error:\n%s", report.String())
	}
}
