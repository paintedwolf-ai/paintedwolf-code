package output_test

import (
	"strings"
	"testing"

	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestParseOutputOpengrepJSONErrorsWithoutFindings(t *testing.T) {
	raw := []byte(`{"results":[],"errors":[{"message":"invalid configuration file found"}]}`)
	_, err := scanoutput.ParseOutput(scanoutput.OutputParserOpengrepJSON, raw)
	if err == nil {
		t.Fatal("expected error when opengrep reports config errors with zero results")
	}
}

func TestParseOutputOpengrepJSONRuleParseErrorWithoutFindings(t *testing.T) {
	raw := []byte(`{"results":[],"errors":[{
		"type":"Rule parse error",
		"rule_id":"config.scanners.rules.vendor.apiiro-malicious.obfuscation.php.php_obfuscation_declarations.php-obfuscation-declarations",
		"message":"Rule parse error in rule php-obfuscation-declarations: ` + "`const $VAR = $V;`" + ` was unexpected"
	}]}`)
	res, err := scanoutput.ParseOutput(scanoutput.OutputParserOpengrepJSON, raw)
	testutil.FailErr(t, "scan.ParseOutput failed", err)
	if len(res.Findings) != 0 {
		t.Fatalf("findings = %d, want 0", len(res.Findings))
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("warnings = %d, want 1", len(res.Warnings))
	}
	if res.Warnings[0].Kind != api.ScanWarningRuleParseError {
		t.Fatalf("warning kind = %q", res.Warnings[0].Kind)
	}
	if !strings.Contains(res.Warnings[0].RuleID, "php-obfuscation-declarations") {
		t.Fatalf("rule id = %q", res.Warnings[0].RuleID)
	}
}

// PartialParsing encodes its tag as the first array element.
func TestParseOutputOpengrepJSONPartialParsingWarning(t *testing.T) {
	raw := []byte(`{"results":[],"errors":[{
		"code":3,
		"level":"warn",
		"type":["PartialParsing",[{"path":"server/index.js","start":{"line":41,"col":5,"offset":0},"end":{"line":41,"col":8,"offset":3}}]],
		"path":"server/index.js",
		"message":"Syntax error at line server/index.js:41"
	}]}`)
	res, err := scanoutput.ParseOutput(scanoutput.OutputParserOpengrepJSON, raw)
	testutil.FailErr(t, "scan.ParseOutput failed", err)
	if len(res.Warnings) != 1 || res.Warnings[0].Kind != api.ScanWarningFilePartialParse {
		t.Fatalf("warnings = %+v", res.Warnings)
	}
	if res.Warnings[0].File != "server/index.js" {
		t.Fatalf("file = %q", res.Warnings[0].File)
	}
}

// PartialParsing warnings can accompany usable findings.
func TestParseOutputOpengrepJSONArrayTagPreservesFindings(t *testing.T) {
	raw := []byte(`{
		"results":[{
			"check_id":"lycaon.rust.unsafe-block",
			"path":"src/main.rs",
			"start":{"line":3,"col":1},
			"end":{"line":3,"col":7},
			"extra":{"message":"unsafe block","severity":"ERROR"}
		}],
		"errors":[{
			"type":["PartialParsing",[{"path":"vendor/broken.js","start":{"line":1,"col":1,"offset":0},"end":{"line":1,"col":2,"offset":1}}]],
			"path":"vendor/broken.js",
			"message":"Syntax error at line vendor/broken.js:1"
		}]
	}`)
	res, err := scanoutput.ParseOutput(scanoutput.OutputParserOpengrepJSON, raw)
	testutil.FailErr(t, "scan.ParseOutput failed", err)
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(res.Findings))
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Kind != api.ScanWarningFilePartialParse {
		t.Fatalf("warnings = %+v", res.Warnings)
	}
}

func TestParseOutputOpengrepJSONEmpty(t *testing.T) {
	_, err := scanoutput.ParseOutput(scanoutput.OutputParserOpengrepJSON, nil)
	if err == nil {
		t.Fatal("expected error for empty opengrep JSON")
	}
	if !strings.Contains(err.Error(), "empty output") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseOutputOpengrepJSONInvalidReportsSize(t *testing.T) {
	raw := []byte(`{"results":[`)
	_, err := scanoutput.ParseOutput(scanoutput.OutputParserOpengrepJSON, raw)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "12 bytes") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseOutputOpengrepJSON(t *testing.T) {
	raw := []byte(`{
		"results": [{
			"check_id": "lycaon.rust.unsafe-block",
			"path": "src/main.rs",
			"start": {"line": 3, "col": 1},
			"end": {"line": 3, "col": 7},
			"extra": {"message": "unsafe block", "severity": "ERROR"}
		}]
	}`)
	res, err := scanoutput.ParseOutput(scanoutput.OutputParserOpengrepJSON, raw)
	testutil.FailErr(t, "scan.ParseOutput failed", err)
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %d", len(res.Findings))
	}
	if res.Findings[0].RuleID != "opengrep:lycaon.rust.unsafe-block" {
		t.Fatalf("rule id = %q", res.Findings[0].RuleID)
	}
	if res.Findings[0].Fingerprints.Primary == "" {
		t.Fatal("expected fingerprints.primary")
	}
	if res.Findings[0].Properties == nil || res.Findings[0].Properties.Lycaon == nil ||
		res.Findings[0].Properties.Lycaon.Kind != api.FindingKindSAST {
		t.Fatalf("kind = %#v", res.Findings[0].Properties)
	}
}

func TestSARIFParserRegistered(t *testing.T) {
	if !scanoutput.IsRegisteredOutputParser(scanoutput.OutputParserSARIF) {
		t.Fatal("sarif parser not registered")
	}
}

// Covers the minimal interoperable SARIF result shape.
func TestParseOutputSARIFCommunitySubsetGolden(t *testing.T) {
	raw := []byte(`{
		"version": "2.1.0",
		"runs": [{
			"tool": {"driver": {"name": "communitytool", "semanticVersion": "1.2.3"}},
			"results": [
				{
					"ruleId": "hardcoded-password",
					"level": "error",
					"message": {"text": "hardcoded password detected"},
					"locations": [{
						"physicalLocation": {
							"artifactLocation": {"uri": "src/auth.go"},
							"region": {"startLine": 21, "startColumn": 3}
						}
					}]
				},
				{
					"ruleId": "todo-comment",
					"level": "note",
					"message": {"text": "TODO left in code"}
				}
			]
		}]
	}`)
	res, err := scanoutput.ParseOutput(scanoutput.OutputParserSARIF, raw)
	testutil.FailErr(t, "scan.ParseOutput failed", err)
	if res.FindingsCount != 2 || len(res.Findings) != 2 {
		t.Fatalf("findings = %d", len(res.Findings))
	}
	first := res.Findings[0]
	if first.RuleID != "hardcoded-password" {
		t.Fatalf("rule id = %q", first.RuleID)
	}
	if first.Level != api.FindingLevelHigh {
		t.Fatalf("level = %q", first.Level)
	}
	if first.Message != "hardcoded password detected" {
		t.Fatalf("message = %q", first.Message)
	}
	if first.Locations[0].URI != "src/auth.go" || first.Locations[0].StartLine != 21 {
		t.Fatalf("location = %+v", first.Locations[0])
	}
	if first.Tool.Name != "communitytool" || first.Tool.Version != "1.2.3" {
		t.Fatalf("tool = %+v", first.Tool)
	}
	if first.Fingerprints.Primary == "" {
		t.Fatal("expected fingerprints.primary")
	}
	if res.Findings[1].Level != api.FindingLevelLow {
		t.Fatalf("note level = %q", res.Findings[1].Level)
	}
}

func TestParseOutputSARIFInvalid(t *testing.T) {
	if _, err := scanoutput.ParseOutput(scanoutput.OutputParserSARIF, []byte(`{"runs":[`)); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestParseOutputSARIFUsesRuleDefaultConfigurationLevel(t *testing.T) {
	raw := []byte(`{
		"version":"2.1.0",
		"runs":[{
			"tool":{"driver":{"name":"tool","rules":[{
				"id":"critical-rule","shortDescription":{"text":"critical"},
				"defaultConfiguration":{"level":"error"}
			}]}},
			"results":[{"ruleIndex":0,"message":{"text":"found"}}]
		}]
	}`)
	res, err := scanoutput.ParseOutput(scanoutput.OutputParserSARIF, raw)
	testutil.FailErr(t, "scan.ParseOutput failed", err)
	if len(res.Findings) != 1 || res.Findings[0].RuleID != "critical-rule" || res.Findings[0].Level != api.FindingLevelHigh {
		t.Fatalf("findings = %#v", res.Findings)
	}
}

func TestParseOutputSARIFUsesDriverVersionAndMarkdownMessage(t *testing.T) {
	raw := []byte(`{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"scanner","version":"4.2.0"}},"results":[{"ruleId":"rule","message":{"markdown":"**finding**"}}]}]}`)
	res, err := scanoutput.ParseOutput(scanoutput.OutputParserSARIF, raw)
	testutil.FailErr(t, "scan.ParseOutput failed", err)
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %d want 1", len(res.Findings))
	}
	finding := res.Findings[0]
	if finding.Tool.Version != "4.2.0" || finding.Message != "**finding**" {
		t.Fatalf("finding = %#v", finding)
	}
}

func TestParseOutputSARIFIgnoresTopLevelProductProperties(t *testing.T) {
	raw := []byte(`{
		"version":"2.1.0",
		"runs":[{
			"tool":{"driver":{"name":"scanner"}},
			"results":[{
				"ruleId":"rule",
				"message":{"text":"finding"},
				"properties":{"kind":"sca","categories":["sca"],"advisory":{"osv_id":"OSV-1"}}
			}]
		}]
	}`)
	res, err := scanoutput.ParseOutput(scanoutput.OutputParserSARIF, raw)
	testutil.FailErr(t, "scan.ParseOutput failed", err)
	finding := res.Findings[0]
	if finding.Properties.Lycaon.Kind != api.FindingKindCustom || finding.Properties.Lycaon.Advisory != nil {
		t.Fatalf("product properties = %#v", finding.Properties.Lycaon)
	}
	if len(finding.Properties.Lycaon.Categories) != 1 || finding.Properties.Lycaon.Categories[0] != api.ScanCategorySecurity {
		t.Fatalf("categories = %#v", finding.Properties.Lycaon.Categories)
	}
}

func TestParseOutputSARIFWithDateOnlyTaxonomyRelease(t *testing.T) {
	raw := []byte(`{"version":"2.1.0","runs":[{
  "tool":{"driver":{"name":"gosec"}},
  "taxonomies":[{"name":"CWE","releaseDateUtc":"2021-03-15"}],
  "results":[{"ruleId":"G101","message":{"text":"Hardcoded credentials"},"level":"error"}]
 }]}`)
	result, err := scanoutput.ParseOutput(scanoutput.OutputParserSARIF, raw)
	testutil.FailErr(t, "parse SARIF with taxonomy release date", err)
	if len(result.Findings) != 1 || result.Findings[0].RuleID != "G101" {
		t.Fatalf("findings = %+v", result.Findings)
	}
}
