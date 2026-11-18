package scantest_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
)

func TestValidateRuleFile_valid(t *testing.T) {
	path := filepath.Join("testdata", "valid.yaml")
	if err := scantest.ValidateRuleFile(path); err != nil {
		testutil.FailErr(t, "scantest.ValidateRuleFile failed", err)
	}
}

func TestValidateRuleFile_rejectsBadID(t *testing.T) {
	path := filepath.Join("testdata", "bad-id.yaml")
	err := scantest.ValidateRuleFile(path)
	if err == nil {
		t.Fatal("expected error for bad id prefix")
	}
}

func TestValidateRuleFile_rejectsInfoSeverity(t *testing.T) {
	path := filepath.Join("testdata", "info-severity.yaml")
	err := scantest.ValidateRuleFile(path)
	if err == nil {
		t.Fatal("expected error for INFO severity")
	}
}

func TestValidateRuleFile_rejectsMissingLanguages(t *testing.T) {
	path := filepath.Join("testdata", "missing-languages.yaml")
	err := scantest.ValidateRuleFile(path)
	if err == nil {
		t.Fatal("expected error for missing languages")
	}
}
