package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/owenrumney/go-sarif/v2/sarif"
)

// Exports render a ledger query as SARIF, with ignores as suppressions, or as OpenVEX.

// ExportLedgerSARIF writes the matched entries as SARIF 2.1, with each
// project decision attached to the result it covers as a suppression.
func ExportLedgerSARIF(entries []api.FindingLedgerEntry) ([]byte, error) {
	report, err := sarif.New(sarif.Version210)
	if err != nil {
		return nil, err
	}
	findings := make([]api.SecurityFinding, 0, len(entries))
	suppressions := make(map[string]*api.FindingIgnore, len(entries))
	for _, entry := range entries {
		findings = append(findings, entry.Finding)
		if entry.Ignore != nil && !entry.Ignore.Expired {
			suppressions[entry.Finding.Fingerprints.Primary] = entry.Ignore
		}
	}
	groups := groupFindingsByTool(findings)
	if len(groups) == 0 {
		report.AddRun(newSARIFRun(api.ToolDescriptor{Name: sarifExportToolName}, nil, false))
	}
	for _, group := range groups {
		run := newSARIFRun(group.tool, group.findings, false)
		for i, finding := range group.findings {
			ignore, ok := suppressions[finding.Fingerprints.Primary]
			if !ok || i >= len(run.Results) {
				continue
			}
			run.Results[i].Suppressions = []*sarif.Suppression{sarifSuppression(ignore)}
		}
		report.AddRun(run)
	}
	var buf bytes.Buffer
	if err := report.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// sarifSuppression renders an ignore as an external SARIF suppression.
func sarifSuppression(ignore *api.FindingIgnore) *sarif.Suppression {
	suppression := sarif.NewSuppression("external").WithStatus("accepted")
	justification := strings.TrimSpace(ignore.Reason)
	if vex := strings.TrimSpace(string(ignore.Justification)); vex != "" {
		justification = strings.TrimSpace(vex + ": " + justification)
	}
	if justification != "" {
		suppression = suppression.WithJustifcation(justification)
	}
	if id := strings.TrimSpace(ignore.EntryID); id != "" {
		suppression.PropertyBag = sarif.PropertyBag{Properties: sarif.Properties{
			"lycaon": map[string]any{
				"entry_id":   id,
				"matched_on": ignore.MatchedOn,
				"expires":    ignore.ExpiresOn,
			},
		}}
	}
	return suppression
}

// OpenVEX document shapes.
type vexDocument struct {
	Context    string         `json:"@context"`
	ID         string         `json:"@id"`
	Author     string         `json:"author"`
	Timestamp  string         `json:"timestamp"`
	Version    int            `json:"version"`
	Tooling    string         `json:"tooling"`
	Statements []vexStatement `json:"statements"`
}

type vexStatement struct {
	Vulnerability vexVulnerability `json:"vulnerability"`
	Products      []vexProduct     `json:"products,omitempty"`
	Status        string           `json:"status"`
	Justification string           `json:"justification,omitempty"`
	StatusNotes   string           `json:"status_notes,omitempty"`
	Timestamp     string           `json:"timestamp,omitempty"`
}

type vexVulnerability struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
}

type vexProduct struct {
	ID string `json:"@id"`
}

const (
	vexContext     = "https://openvex.dev/ns/v0.2.0"
	vexTooling     = "Painted Wolf Code"
	vexStatusFixed = "fixed"
)

// ExportLedgerOpenVEX writes statements for advisory findings only: justified
// ignores are not_affected, present findings affected, fixed ones fixed.
// not_observed and unverified are omitted.
func ExportLedgerOpenVEX(entries []api.FindingLedgerEntry, author string, now time.Time) ([]byte, error) {
	stamp := now.UTC().Format(time.RFC3339)
	doc := vexDocument{
		Context:   vexContext,
		ID:        "https://openvex.dev/docs/painted-wolf/" + stamp,
		Author:    strings.TrimSpace(author),
		Timestamp: stamp,
		Version:   1,
		Tooling:   vexTooling,
	}
	if doc.Author == "" {
		doc.Author = vexTooling
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		statement, ok := vexStatementFor(entry, stamp)
		if !ok {
			continue
		}
		key := statement.Vulnerability.Name + "|" + statement.Status + "|" + statement.Justification
		if seen[key] {
			continue
		}
		seen[key] = true
		doc.Statements = append(doc.Statements, statement)
	}
	if doc.Statements == nil {
		doc.Statements = []vexStatement{}
	}
	return json.MarshalIndent(doc, "", "  ")
}

func vexStatementFor(entry api.FindingLedgerEntry, stamp string) (vexStatement, bool) {
	advisory := advisoryOf(entry.Finding)
	if advisory == nil {
		return vexStatement{}, false
	}
	name, aliases := vexVulnerabilityIDs(advisory)
	if name == "" {
		return vexStatement{}, false
	}
	statement := vexStatement{
		Vulnerability: vexVulnerability{Name: name, Aliases: aliases},
		Timestamp:     stamp,
	}
	if product := vexProductFor(advisory); product != "" {
		statement.Products = []vexProduct{{ID: product}}
	}
	switch {
	case entry.Ignore != nil && !entry.Ignore.Expired && entry.Ignore.Justification != "":
		statement.Status = "not_affected"
		statement.Justification = string(entry.Ignore.Justification)
		statement.StatusNotes = entry.Ignore.Reason
	case entry.State == api.FindingLedgerFixed:
		statement.Status = vexStatusFixed
	case entry.State == api.FindingLedgerOpen ||
		entry.State == api.FindingLedgerReopened ||
		entry.State == api.FindingLedgerIgnored:
		statement.Status = "affected"
		if entry.Ignore != nil && !entry.Ignore.Expired {
			// Without a justification the ignore stays affected; the reason becomes a note.
			statement.StatusNotes = entry.Ignore.Reason
		}
	default:
		// No VEX status fits an absence that established nothing.
		return vexStatement{}, false
	}
	return statement, true
}

// vexVulnerabilityIDs names the statement by CVE when one exists; other ids become aliases.
func vexVulnerabilityIDs(advisory *api.AdvisoryRef) (string, []string) {
	ids := scanfindings.AdvisoryIDs(advisory)
	if len(ids) == 0 {
		return "", nil
	}
	name := ids[0]
	for _, id := range ids {
		if strings.HasPrefix(strings.ToUpper(id), "CVE-") {
			name = id
			break
		}
	}
	aliases := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != name {
			aliases = append(aliases, strings.ToUpper(id))
		}
	}
	return strings.ToUpper(name), aliases
}

// vexProductFor builds a package URL when the advisory names a package and ecosystem.
func vexProductFor(advisory *api.AdvisoryRef) string {
	if advisory.Package == nil {
		return ""
	}
	name := strings.TrimSpace(advisory.Package.Name)
	if name == "" {
		return ""
	}
	ecosystem := scanfindings.NormalizePackageEcosystem(advisory.Package.Ecosystem)
	if ecosystem == "" {
		return ""
	}
	purl := fmt.Sprintf("pkg:%s/%s", strings.ToLower(ecosystem), name)
	if version := strings.TrimSpace(advisory.Package.Version); version != "" {
		purl += "@" + version
	}
	return purl
}

func advisoryOf(finding api.SecurityFinding) *api.AdvisoryRef {
	if finding.Properties == nil || finding.Properties.Lycaon == nil {
		return nil
	}
	return finding.Properties.Lycaon.Advisory
}
