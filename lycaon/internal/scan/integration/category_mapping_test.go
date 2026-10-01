package integration

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCategoryEvidenceMappingContract(t *testing.T) {
	securityCategories := []api.ScanCategory{
		api.ScanCategorySecurity,
		api.ScanCategorySAST,
		api.ScanCategorySCA,
		api.ScanCategoryContainer,
		api.ScanCategorySecret,
	}
	verifyCategories := []api.ScanCategory{
		api.ScanCategoryLint,
		api.ScanCategoryTypes,
		api.ScanCategoryStyle,
	}

	for _, c := range securityCategories {
		if wantEvidenceType(c) != evidence.GateTypeSecurity {
			t.Fatalf("category %q should map to security evidence", c)
		}
	}
	for _, c := range verifyCategories {
		if wantEvidenceType(c) != evidence.GateTypeVerify {
			t.Fatalf("category %q should map to verify evidence", c)
		}
	}

	// License and custom map to no evidence type.
	if wantEvidenceType(api.ScanCategoryLicense) != "" {
		t.Fatal("license category mapping is not defined yet")
	}
	if wantEvidenceType(api.ScanCategoryCustom) != "" {
		t.Fatal("custom category mapping is not defined yet")
	}
}

func wantEvidenceType(c api.ScanCategory) evidence.GateType {
	switch c {
	case api.ScanCategorySecurity, api.ScanCategorySAST, api.ScanCategorySCA,
		api.ScanCategoryContainer, api.ScanCategorySecret:
		return evidence.GateTypeSecurity
	case api.ScanCategoryLint, api.ScanCategoryTypes, api.ScanCategoryStyle:
		return evidence.GateTypeVerify
	default:
		return ""
	}
}
