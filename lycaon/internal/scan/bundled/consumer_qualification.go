package bundled

import (
	"encoding/json"
	"fmt"
	"path/filepath"
)

// consumerQualification is the admission projection of the product-owned report.
type consumerQualification struct {
	SchemaVersion int    `json:"schema_version"`
	Status        string `json:"status"`
	Code          string `json:"code"`
	Error         string `json:"error"`
	GOOS          string `json:"goos"`
	GOARCH        string `json:"goarch"`
	BinarySHA256  string `json:"binary_sha256"`
	BinaryBytes   int64  `json:"binary_bytes"`
	Confined      bool   `json:"confined"`
	Cases         []struct {
		File         string `json:"file"`
		ExpectedRule string `json:"expected_rule"`
		Findings     int    `json:"findings"`
		Passed       bool   `json:"passed"`
	} `json:"cases"`
}

func verifyConsumerQualification(raw json.RawMessage, identity *BuildIdentity) error {
	var report consumerQualification
	if err := json.Unmarshal(raw, &report); err != nil {
		return err
	}
	if report.SchemaVersion != 1 || report.Status != "passed" || report.Code != "" || report.Error != "" || !report.Confined || report.GOOS != identity.GOOS || report.GOARCH != identity.GOARCH || report.BinarySHA256 != identity.BinarySHA256 || report.BinaryBytes != identity.BinaryBytes {
		return fmt.Errorf("consumer qualification must pass under confinement for the exact packaged identity")
	}
	seen := make(map[string]bool)
	positive, negative := false, false
	for _, row := range report.Cases {
		if row.File == "" || filepath.Base(row.File) != row.File || row.File == "." || seen[row.File] || !row.Passed {
			return fmt.Errorf("consumer qualification has invalid or failed product cases")
		}
		seen[row.File] = true
		if row.ExpectedRule == "" {
			if row.Findings != 0 {
				return fmt.Errorf("consumer negative control produced findings")
			}
			negative = true
		} else {
			if row.Findings != 1 {
				return fmt.Errorf("consumer positive control requires its finding")
			}
			positive = true
		}
	}
	if !positive || !negative {
		return fmt.Errorf("consumer qualification requires positive and negative product controls")
	}
	return nil
}
