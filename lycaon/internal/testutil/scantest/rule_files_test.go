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

func TestCollectRuleIDsFromDirAndProvenance(t *testing.T) {
	ids, err := scantest.CollectRuleIDsFromDir("testdata")
	testutil.FailErr(t, "CollectRuleIDsFromDir failed", err)
	if len(ids) == 0 {
		t.Fatal("expected collected rule IDs")
	}
	if _, err := scantest.LoadRuleFile(filepath.Join("testdata", "does-not-exist.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
	if err := scantest.ValidateRuleFile(filepath.Join("testdata", "does-not-exist.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
	cfg, err := scantest.LoadRulesProvenance()
	testutil.FailErr(t, "LoadRulesProvenance failed", err)
	if cfg == nil || len(cfg.Vendors) == 0 {
		t.Fatalf("LoadRulesProvenance = %+v", cfg)
	}
}
