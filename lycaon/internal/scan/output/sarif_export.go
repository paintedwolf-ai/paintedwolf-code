package output

import (
	"bytes"
	"strings"

	"github.com/google/uuid"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/owenrumney/go-sarif/v2/sarif"
)

const sarifExportToolName = "painted-wolf-code"

// ExportScanSARIF builds a SARIF 2.1 document from a completed code scan row.
func ExportScanSARIF(scan *api.CodeScan) ([]byte, error) {
	if scan == nil {
		return ExportSARIF(nil, false)
	}
	return ExportSARIF(scan.Findings, false)
}

// ExportSARIF builds a SARIF 2.1 document from normalized scan findings.
func ExportSARIF(findings []api.SecurityFinding, truncated bool) ([]byte, error) {
	report, err := sarif.New(sarif.Version210)
	if err != nil {
		return nil, err
	}
	groups := groupFindingsByTool(findings)
	if len(groups) == 0 {
		run := newSARIFRun(api.ToolDescriptor{Name: sarifExportToolName}, nil, truncated)
		report.AddRun(run)
	} else {
		for _, g := range groups {
			report.AddRun(newSARIFRun(g.tool, g.findings, truncated))
		}
	}
	var buf bytes.Buffer
	if err := report.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type sarifToolGroup struct {
	tool     api.ToolDescriptor
	findings []api.SecurityFinding
}

func groupFindingsByTool(findings []api.SecurityFinding) []sarifToolGroup {
	if len(findings) == 0 {
		return nil
	}
	order := make([]string, 0, 4)
	byDriver := map[string]*sarifToolGroup{}
	for _, f := range findings {
		driverID := strings.TrimSpace(f.Tool.DriverID)
		if driverID == "" {
			driverID = sarifExportToolName
		}
		g, ok := byDriver[driverID]
		if !ok {
			tool := f.Tool
			if strings.TrimSpace(tool.Name) == "" {
				tool.Name = driverID
			}
			if strings.TrimSpace(tool.DriverID) == "" {
				tool.DriverID = driverID
			}
			g = &sarifToolGroup{tool: tool}
			byDriver[driverID] = g
			order = append(order, driverID)
		}
		g.findings = append(g.findings, f)
	}
	out := make([]sarifToolGroup, 0, len(order))
	for _, id := range order {
		out = append(out, *byDriver[id])
	}
	return out
}

func newSARIFRun(tool api.ToolDescriptor, findings []api.SecurityFinding, truncated bool) *sarif.Run {
	name := strings.TrimSpace(tool.Name)
	if name == "" {
		name = sarifExportToolName
	}
	driver := &sarif.ToolComponent{
		Name:  name,
		Rules: sarifRulesFromFindings(findings),
	}
	if v := strings.TrimSpace(tool.Version); v != "" {
		driver.Version = &v
	}
	if id := strings.TrimSpace(tool.DriverID); id != "" {
		driver.Properties = sarif.Properties{"lycaon": map[string]any{"driver_id": id}}
		if parsed, err := uuid.Parse(id); err == nil {
			guid := parsed.String()
			driver.GUID = &guid
		}
	}
	run := sarif.NewRun(sarif.Tool{Driver: driver})
	if truncated {
		run.Invocations = []*sarif.Invocation{{
			ExecutionSuccessful: boolPtr(true),
		}}
		run.Properties = sarif.Properties{
			"truncated": true,
		}
	}
	for _, finding := range findings {
		run.Results = append(run.Results, sarifResultFromFinding(finding))
	}
	return run
}

func sarifRulesFromFindings(findings []api.SecurityFinding) []*sarif.ReportingDescriptor {
	seen := make(map[string]struct{}, len(findings))
	rules := make([]*sarif.ReportingDescriptor, 0, len(findings))
	for _, finding := range findings {
		ruleID := strings.TrimSpace(finding.RuleID)
		if ruleID == "" {
			ruleID = "scan:unknown"
		}
		if _, ok := seen[ruleID]; ok {
			continue
		}
		seen[ruleID] = struct{}{}
		description := strings.TrimSpace(finding.Message)
		if line, _, ok := strings.Cut(description, "\n"); ok {
			description = strings.TrimSpace(line)
		}
		if description == "" {
			description = ruleID
		}
		if len(description) > 256 {
			description = description[:256]
		}
		level := sarifLevelFromFinding(finding.Level)
		rule := sarif.NewRule(ruleID).
			WithDescription(description).
			WithDefaultConfiguration(&sarif.ReportingConfiguration{Level: *level})
		rules = append(rules, rule)
	}
	return rules
}

func sarifResultFromFinding(finding api.SecurityFinding) *sarif.Result {
	ruleID := strings.TrimSpace(finding.RuleID)
	if ruleID == "" {
		ruleID = "scan:unknown"
	}
	message := strings.TrimSpace(finding.Message)
	if message == "" {
		message = ruleID
	}
	result := &sarif.Result{
		RuleID: &ruleID,
		Level:  sarifLevelFromFinding(finding.Level),
		Message: sarif.Message{
			Text: &message,
		},
	}
	if fp := strings.TrimSpace(finding.Fingerprints.Primary); fp != "" {
		result.Fingerprints = map[string]interface{}{"primary": fp}
	}
	if props := sarifResultProperties(finding); len(props) > 0 {
		result.Properties = props
	}
	result.CodeFlows = sarifDataflow(finding.Dataflow)
	for _, location := range finding.Locations {
		uri := strings.TrimSpace(location.URI)
		if uri == "" {
			continue
		}
		region := &sarif.Region{}
		if location.StartLine > 0 {
			region.StartLine = intPtr(location.StartLine)
		}
		if location.StartColumn > 0 {
			region.StartColumn = intPtr(location.StartColumn)
		}
		if location.EndLine > 0 {
			region.EndLine = intPtr(location.EndLine)
		}
		if location.EndColumn > 0 {
			region.EndColumn = intPtr(location.EndColumn)
		}
		physical := &sarif.PhysicalLocation{ArtifactLocation: &sarif.ArtifactLocation{URI: &uri}}
		if region.StartLine != nil || region.StartColumn != nil || region.EndLine != nil || region.EndColumn != nil {
			physical.Region = region
		}
		result.Locations = append(result.Locations, &sarif.Location{
			PhysicalLocation: &sarif.PhysicalLocation{
				ArtifactLocation: physical.ArtifactLocation,
				Region:           physical.Region,
			},
		})
	}
	return result
}

func sarifResultProperties(finding api.SecurityFinding) sarif.Properties {
	props := sarif.Properties{}
	if finding.Properties == nil || finding.Properties.Lycaon == nil {
		return props
	}
	ly := map[string]any{}
	if kind := string(scanfindings.FindingKind(finding)); kind != "" {
		ly["kind"] = kind
	}
	if level := string(finding.Level); level != "" {
		ly["finding_level"] = level
	}
	if code := strings.TrimSpace(finding.Properties.Lycaon.HintCode); code != "" {
		ly["hint_code"] = code
	}
	if len(finding.Properties.Lycaon.Categories) > 0 {
		ly["categories"] = finding.Properties.Lycaon.Categories
	}
	if disposition := string(finding.Properties.Lycaon.Disposition); disposition != "" {
		ly["disposition"] = disposition
	}
	if len(finding.Properties.Lycaon.Sources) > 0 {
		ly["sources"] = finding.Properties.Lycaon.Sources
	}
	if adv := finding.Properties.Lycaon.Advisory; adv != nil {
		block := map[string]any{}
		if id := strings.TrimSpace(adv.OSVID); id != "" {
			block["osv_id"] = id
		}
		if len(adv.CVEIDs) > 0 {
			block["cve_ids"] = adv.CVEIDs
		}
		if len(adv.GHSAIDs) > 0 {
			block["ghsa_ids"] = adv.GHSAIDs
		}
		if len(adv.Aliases) > 0 {
			block["aliases"] = adv.Aliases
		}
		if adv.Kind != "" {
			block["kind"] = string(adv.Kind)
		}
		if adv.Package != nil {
			packageBlock := map[string]any{}
			if name := strings.TrimSpace(adv.Package.Name); name != "" {
				packageBlock["name"] = name
			}
			if version := strings.TrimSpace(adv.Package.Version); version != "" {
				packageBlock["version"] = version
			}
			if ecosystem := scanfindings.NormalizePackageEcosystem(adv.Package.Ecosystem); ecosystem != "" {
				packageBlock["ecosystem"] = ecosystem
			}
			if len(packageBlock) > 0 {
				block["package"] = packageBlock
			}
		}
		if len(block) > 0 {
			ly["advisory"] = block
		}
	}
	if len(ly) > 0 {
		props["lycaon"] = ly
	}
	return props
}

func sarifLevelFromFinding(level api.FindingLevel) *string {
	switch level {
	case api.FindingLevelCritical, api.FindingLevelHigh:
		v := "error"
		return &v
	case api.FindingLevelMedium:
		v := "warning"
		return &v
	case api.FindingLevelLow:
		v := "note"
		return &v
	default:
		v := "none"
		return &v
	}
}

func boolPtr(v bool) *bool { return &v }

func intPtr(v int) *int { return &v }
