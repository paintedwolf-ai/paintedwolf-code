package output

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/advisory"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/owenrumney/go-sarif/v2/sarif"
)

type sarifParser struct{}

func newSARIFParser() OutputParser { return sarifParser{} }

func (sarifParser) ID() string { return OutputParserSARIF }

func (sarifParser) Parse(raw []byte) (*Result, error) {
	// Decode the finding surface; optional taxonomy metadata is not evidence.
	var report struct {
		Runs []struct {
			Tool    sarif.Tool      `json:"tool"`
			Results []*sarif.Result `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("sarif: %w", err)
	}
	result := &Result{
		Categories: []api.ScanCategory{api.ScanCategorySecurity},
	}
	for _, parsed := range report.Runs {
		run := &sarif.Run{Tool: parsed.Tool, Results: parsed.Results}
		tool := sarifToolDescriptor(run)
		for _, res := range run.Results {
			finding, err := sarifResultToFinding(res, run, tool)
			if err != nil {
				return nil, err
			}
			result.Findings = append(result.Findings, finding)
			result.Categories = mergeScanCategories(result.Categories, findingCategories(finding)...)
		}
	}
	result.FindingsCount = len(result.Findings)
	return result, nil
}

func sarifToolDescriptor(run *sarif.Run) api.ToolDescriptor {
	driverID := "sarif"
	name := "SARIF"
	version := ""
	explicitDriverID := false
	if run != nil && run.Tool.Driver != nil {
		d := run.Tool.Driver
		if raw, ok := d.Properties["lycaon"].(map[string]interface{}); ok {
			if id := stringProperty(raw["driver_id"]); id != "" {
				driverID = id
				explicitDriverID = true
			}
		}
		if strings.TrimSpace(d.Name) != "" {
			name = strings.TrimSpace(d.Name)
			if driverID == "sarif" {
				driverID = strings.TrimSpace(d.Name)
			}
		}
		if d.FullName != nil && strings.TrimSpace(*d.FullName) != "" {
			name = strings.TrimSpace(*d.FullName)
		}
		if !explicitDriverID && d.GUID != nil && strings.TrimSpace(*d.GUID) != "" {
			driverID = strings.TrimSpace(*d.GUID)
		}
		if d.SemanticVersion != nil {
			version = strings.TrimSpace(*d.SemanticVersion)
		} else if d.Version != nil {
			version = strings.TrimSpace(*d.Version)
		}
	}
	return api.ToolDescriptor{DriverID: driverID, Name: name, Version: version}
}

func sarifResultToFinding(res *sarif.Result, run *sarif.Run, tool api.ToolDescriptor) (api.SecurityFinding, error) {
	if res == nil {
		return api.SecurityFinding{}, fmt.Errorf("sarif: nil result")
	}
	ruleID := derefString(res.RuleID)
	if strings.TrimSpace(ruleID) == "" && res.Rule != nil {
		ruleID = derefString(res.Rule.Id)
	}
	if strings.TrimSpace(ruleID) == "" {
		if descriptor := sarifRuleDescriptor(res, run); descriptor != nil {
			ruleID = descriptor.ID
		}
	}
	msg := derefString(res.Message.Text)
	if strings.TrimSpace(msg) == "" {
		msg = derefString(res.Message.Markdown)
	}
	level := sarifResultLevel(res, run)
	locs := sarifLocations(res.Locations)
	kind := sarifKindHint(res)
	categories := sarifCategories(res)
	advisoryRef := sarifAdvisory(res)
	primary := ""
	if res.Fingerprints != nil {
		primary = fingerprintValue(res.Fingerprints["primary"])
	}
	finding := scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
		DriverID:    tool.DriverID,
		ToolName:    tool.Name,
		ToolVersion: tool.Version,
		RuleID:      ruleID,
		Level:       level,
		Message:     msg,
		Kind:        kind,
		Locations:   locs,
		Advisory:    advisoryRef,
		Categories:  categories,
	})
	applySARIFLycaonProperties(&finding, res)
	if primary != "" {
		finding.Fingerprints.Primary = primary
	}
	return finding, nil
}

func sarifResultLevel(res *sarif.Result, run *sarif.Run) api.FindingLevel {
	if res != nil && res.Level != nil && strings.TrimSpace(*res.Level) != "" {
		return scanfindings.NormalizeSARIFLevel(*res.Level)
	}
	if rule := sarifRuleDescriptor(res, run); rule != nil && rule.DefaultConfiguration != nil {
		if level := strings.TrimSpace(rule.DefaultConfiguration.Level); level != "" {
			return scanfindings.NormalizeSARIFLevel(level)
		}
	}
	return api.FindingLevelUnknown
}

func applySARIFLycaonProperties(finding *api.SecurityFinding, res *sarif.Result) {
	if finding == nil || finding.Properties == nil || finding.Properties.Lycaon == nil {
		return
	}
	props := sarifLycaonProperties(res)
	level := api.FindingLevel(stringProperty(props["finding_level"]))
	switch level {
	case api.FindingLevelCritical, api.FindingLevelHigh, api.FindingLevelMedium,
		api.FindingLevelLow, api.FindingLevelInfo, api.FindingLevelUnknown:
		finding.Level = level
	}
	if code := stringProperty(props["hint_code"]); code != "" {
		finding.Properties.Lycaon.HintCode = code
	}
	if sources := stringSliceProperty(props["sources"]); len(sources) > 0 {
		finding.Properties.Lycaon.Sources = sources
	}
	disposition := api.FindingDisposition(stringProperty(props["disposition"]))
	switch disposition {
	case api.FindingDispositionFix, api.FindingDispositionFP, api.FindingDispositionDefer,
		api.FindingDispositionAcceptedRisk, api.FindingDispositionHuman:
		finding.Properties.Lycaon.Disposition = disposition
	}
}

func sarifRuleDescriptor(res *sarif.Result, run *sarif.Run) *sarif.ReportingDescriptor {
	if res == nil || run == nil || run.Tool.Driver == nil {
		return nil
	}
	rules := run.Tool.Driver.Rules
	if res.RuleIndex != nil && int(*res.RuleIndex) < len(rules) {
		return rules[*res.RuleIndex]
	}
	if res.Rule != nil && res.Rule.Index != nil && int(*res.Rule.Index) < len(rules) {
		return rules[*res.Rule.Index]
	}
	id := strings.TrimSpace(derefString(res.RuleID))
	if id == "" && res.Rule != nil {
		id = strings.TrimSpace(derefString(res.Rule.Id))
	}
	for _, rule := range rules {
		if rule != nil && rule.ID == id {
			return rule
		}
	}
	return nil
}

func sarifLocations(in []*sarif.Location) []api.SecurityFindingLocation {
	if len(in) == 0 {
		return []api.SecurityFindingLocation{{URI: ""}}
	}
	out := make([]api.SecurityFindingLocation, 0, len(in))
	for _, loc := range in {
		if loc == nil || loc.PhysicalLocation == nil {
			continue
		}
		pl := loc.PhysicalLocation
		uri := ""
		if pl.ArtifactLocation != nil {
			uri = derefString(pl.ArtifactLocation.URI)
		}
		row := api.SecurityFindingLocation{URI: uri}
		if pl.Region != nil {
			if pl.Region.StartLine != nil {
				row.StartLine = *pl.Region.StartLine
			}
			if pl.Region.StartColumn != nil {
				row.StartColumn = *pl.Region.StartColumn
			}
			if pl.Region.EndLine != nil {
				row.EndLine = *pl.Region.EndLine
			}
			if pl.Region.EndColumn != nil {
				row.EndColumn = *pl.Region.EndColumn
			}
		}
		out = append(out, row)
	}
	if len(out) == 0 {
		return []api.SecurityFindingLocation{{URI: ""}}
	}
	return out
}

func sarifKindHint(res *sarif.Result) api.FindingKind {
	if kind := propertyKind(sarifLycaonProperties(res)); kind != "" {
		return kind
	}
	return api.FindingKindCustom
}

func sarifCategories(res *sarif.Result) []api.ScanCategory {
	categories := []api.ScanCategory{api.ScanCategorySecurity}
	for _, value := range stringSliceProperty(sarifLycaonProperties(res)["categories"]) {
		category := api.ScanCategory(strings.TrimSpace(value))
		switch category {
		case api.ScanCategorySecurity, api.ScanCategorySAST, api.ScanCategorySecret,
			api.ScanCategorySCA, api.ScanCategoryContainer:
			categories = mergeScanCategories(categories, category)
		case api.ScanCategoryLint, api.ScanCategoryTypes, api.ScanCategoryStyle, api.ScanCategoryLicense,
			api.ScanCategoryCustom:
		}
	}
	return categories
}

func sarifAdvisory(res *sarif.Result) *api.AdvisoryRef {
	raw, ok := sarifLycaonProperties(res)["advisory"].(map[string]interface{})
	if !ok {
		return nil
	}
	adv := &api.AdvisoryRef{
		OSVID:   stringProperty(raw["osv_id"]),
		CVEIDs:  stringSliceProperty(raw["cve_ids"]),
		GHSAIDs: stringSliceProperty(raw["ghsa_ids"]),
		Aliases: stringSliceProperty(raw["aliases"]),
	}
	advisory.Normalize(adv)
	if kind := api.AdvisoryKind(stringProperty(raw["kind"])); slices.Contains(api.AllAdvisoryKindValues(), kind) {
		adv.Kind = kind
	}
	if pkg, ok := raw["package"].(map[string]interface{}); ok {
		adv.Package = &api.AdvisoryPackageRef{
			Name:      stringProperty(pkg["name"]),
			Version:   stringProperty(pkg["version"]),
			Ecosystem: scanfindings.NormalizePackageEcosystem(stringProperty(pkg["ecosystem"])),
		}
		if adv.Package.Name == "" && adv.Package.Version == "" && adv.Package.Ecosystem == "" {
			adv.Package = nil
		}
	}
	if adv.OSVID != "" || adv.Package != nil {
		return adv
	}
	return nil
}

func sarifLycaonProperties(res *sarif.Result) map[string]interface{} {
	if res == nil {
		return nil
	}
	if raw, ok := res.Properties["lycaon"].(map[string]interface{}); ok {
		return raw
	}
	return nil
}

func findingCategories(f api.SecurityFinding) []api.ScanCategory {
	if f.Properties == nil || f.Properties.Lycaon == nil {
		return nil
	}
	return f.Properties.Lycaon.Categories
}

func mergeScanCategories(existing []api.ScanCategory, additions ...api.ScanCategory) []api.ScanCategory {
	out := append([]api.ScanCategory(nil), existing...)
	for _, addition := range additions {
		seen := false
		for _, category := range out {
			if category == addition {
				seen = true
				break
			}
		}
		if !seen && addition != "" {
			out = append(out, addition)
		}
	}
	return out
}

func stringProperty(raw any) string {
	value, _ := raw.(string)
	return strings.TrimSpace(value)
}

func stringSliceProperty(raw any) []string {
	switch values := raw.(type) {
	case []string:
		return append([]string(nil), values...)
	case []interface{}:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text := stringProperty(value); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func fingerprintValue(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func propertyKind(props map[string]interface{}) api.FindingKind {
	raw, _ := props["kind"].(string)
	switch api.FindingKind(strings.TrimSpace(raw)) {
	case api.FindingKindSAST, api.FindingKindSecret, api.FindingKindSCA,
		api.FindingKindContainer, api.FindingKindIaC, api.FindingKindLicense,
		api.FindingKindCustom:
		return api.FindingKind(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
