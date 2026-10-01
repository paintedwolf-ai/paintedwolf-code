package bundled

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
)

type contractExecution struct {
	TimedOut   *bool `json:"timed_out"`
	ReturnCode *int  `json:"returncode"`
}
type contractQualification struct {
	Case                     string                        `json:"case"`
	Passed                   *bool                         `json:"passed"`
	Status                   json.RawMessage               `json:"status"`
	Execution                *contractExecution            `json:"execution"`
	ExitCode                 *int                          `json:"exit_code"`
	TranslationExecution     map[string]*contractExecution `json:"translation_execution"`
	Expected                 json.RawMessage               `json:"expected"`
	Actual                   json.RawMessage               `json:"actual"`
	TracesValid              json.RawMessage               `json:"traces_valid"`
	EvidenceValid            json.RawMessage               `json:"evidence_valid"`
	DiagnosticPathsValid     json.RawMessage               `json:"diagnostic_paths_valid"`
	DiagnosticPositionsValid json.RawMessage               `json:"diagnostic_positions_valid"`
	FindingPositionsValid    json.RawMessage               `json:"finding_positions_valid"`
	FindingsUnique           json.RawMessage               `json:"findings_unique"`
	Contracts                *int                          `json:"contracts"`
	Failed                   *int                          `json:"failed"`
}

func verifyContractReport(filename string, expected map[string]bool) error {
	// #nosec G304 -- fixed qualification report already checked against provenance.
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maxSourceMetadataBytes)
	seen := make(map[string]bool)
	summary := false
	for scanner.Scan() {
		if len(strings.TrimSpace(scanner.Text())) == 0 {
			continue
		}
		if summary {
			return fmt.Errorf("contract report has data after summary")
		}
		var row contractQualification
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return fmt.Errorf("invalid contract row: %w", err)
		}
		if row.Case == "" {
			if row.Contracts == nil || *row.Contracts != len(expected) || row.Failed == nil || *row.Failed != 0 {
				return fmt.Errorf("contract summary is incomplete or failed")
			}
			summary = true
			continue
		}
		if !expected[row.Case] || seen[row.Case] {
			return fmt.Errorf("unexpected or duplicate contract: %s", row.Case)
		}
		seen[row.Case] = true
		if err := row.validate(); err != nil {
			return fmt.Errorf("contract %s: %w", row.Case, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !summary || len(seen) != len(expected) {
		return fmt.Errorf("contract qualification cases or summary missing")
	}
	return nil
}

func (row contractQualification) validate() error {
	if row.Passed == nil || !*row.Passed {
		return fmt.Errorf("contract did not pass")
	}
	if len(row.Status) != 0 && string(row.Status) != "null" && string(row.Status) != `""` {
		return fmt.Errorf("contract has non-success status")
	}
	if !completeContractExecution(row.Execution, false) || row.ExitCode == nil || *row.ExitCode != *row.Execution.ReturnCode {
		return fmt.Errorf("contract execution incomplete or inconsistent")
	}
	for _, valid := range []json.RawMessage{row.TracesValid, row.EvidenceValid, row.DiagnosticPathsValid, row.DiagnosticPositionsValid, row.FindingPositionsValid, row.FindingsUnique} {
		if len(valid) != 0 && !bytes.Equal(bytes.TrimSpace(valid), []byte("true")) {
			return fmt.Errorf("contract evidence invalid")
		}
	}
	if len(row.Expected) != 0 && len(row.Actual) != 0 {
		var expected, actual any
		if err := decodeContractValue(row.Expected, &expected); err != nil {
			return err
		}
		if err := decodeContractValue(row.Actual, &actual); err != nil {
			return err
		}
		if !reflect.DeepEqual(expected, actual) {
			return fmt.Errorf("contract result differs from expectation")
		}
	}
	if strings.HasPrefix(row.Case, "rule-translation/") {
		for _, phase := range []string{"discovery", "translation"} {
			if !completeContractExecution(row.TranslationExecution[phase], true) {
				return fmt.Errorf("contract translation incomplete")
			}
		}
	}
	return nil
}

func completeContractExecution(execution *contractExecution, success bool) bool {
	if execution == nil || execution.TimedOut == nil || *execution.TimedOut || execution.ReturnCode == nil {
		return false
	}
	if success {
		return *execution.ReturnCode == 0
	}
	switch *execution.ReturnCode {
	case 0, 2, 3, 7:
		return true
	}
	return false
}

func decodeContractValue(raw []byte, value *any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(value)
}
