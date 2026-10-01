package findings

import (
	"cmp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/advisory"
	"github.com/lycaon/lycaon/pkg/api"
)

// MergeByAdvisory folds dependency rows that report one vulnerability in one
// package into a single finding. Rows join when they share the package
// coordinate and any advisory id, so an ecosystem database record, its GHSA
// twin, and a second engine's CVE row become one finding keyed by the canonical
// id with the best published severity. Rows of other kinds pass through, with
// exact fingerprint duplicates dropped.
func MergeByAdvisory(findings []api.SecurityFinding) []api.SecurityFinding {
	if len(findings) == 0 {
		return findings
	}
	groups := newAdvisoryGroups(findings)
	out := make([]api.SecurityFinding, 0, len(findings))
	for _, members := range groups.ordered() {
		if !members.advisory {
			out = append(out, findings[members.rows[0]])
			continue
		}
		out = append(out, foldAdvisoryRows(findings, members.rows))
	}
	return out
}

// advisoryGroups is a union-find over "<package>|<id>" nodes: two rows land in
// one group when any id of one is an id of the other for the same package.
type advisoryGroups struct {
	parent map[string]string
	rows   []advisoryGroupRow
}

type advisoryGroupRow struct {
	index int
	key   string // union-find node for advisory rows, a literal key otherwise
	union bool
}

type advisoryGroupMembers struct {
	rows     []int
	advisory bool
}

func newAdvisoryGroups(findings []api.SecurityFinding) *advisoryGroups {
	g := &advisoryGroups{parent: map[string]string{}, rows: make([]advisoryGroupRow, 0, len(findings))}
	for i, f := range findings {
		kind := FindingKind(f)
		if kind != api.FindingKindSCA && kind != api.FindingKindContainer {
			g.rows = append(g.rows, advisoryGroupRow{index: i, key: "passthrough:" + f.Fingerprints.Primary})
			continue
		}
		pkg := advisoryPackageKey(f)
		ids := advisory.IDs(Advisory(f))
		if pkg == "" || len(ids) == 0 {
			g.rows = append(g.rows, advisoryGroupRow{index: i, key: "solo:" + f.Fingerprints.Primary})
			continue
		}
		nodes := make([]string, len(ids))
		for n, id := range ids {
			nodes[n] = pkg + "|" + strings.ToUpper(id)
			g.find(nodes[n])
		}
		for _, node := range nodes[1:] {
			g.union(nodes[0], node)
		}
		g.rows = append(g.rows, advisoryGroupRow{index: i, key: nodes[0], union: true})
	}
	return g
}

func (g *advisoryGroups) find(node string) string {
	root, ok := g.parent[node]
	if !ok {
		g.parent[node] = node
		return node
	}
	for root != g.parent[root] {
		root = g.parent[root]
	}
	for node != root {
		next := g.parent[node]
		g.parent[node] = root
		node = next
	}
	return root
}

func (g *advisoryGroups) union(a, b string) {
	ra, rb := g.find(a), g.find(b)
	if ra != rb {
		g.parent[rb] = ra
	}
}

// ordered returns groups in order of first appearance. Non-advisory keys keep
// only their first row, which is the fingerprint dedupe for passthrough rows.
func (g *advisoryGroups) ordered() []advisoryGroupMembers {
	index := map[string]int{}
	var out []advisoryGroupMembers
	for _, row := range g.rows {
		key := row.key
		if row.union {
			key = "advisory:" + g.find(row.key)
		}
		at, ok := index[key]
		if !ok {
			index[key] = len(out)
			out = append(out, advisoryGroupMembers{rows: []int{row.index}, advisory: row.union})
			continue
		}
		if row.union {
			out[at].rows = append(out[at].rows, row.index)
		}
	}
	return out
}

// foldAdvisoryRows merges one group into a row whose identity is a pure
// function of the group: the winner is the lowest (driver, fingerprint) row so
// the result does not depend on scanner output order, and the merged advisory
// is re-keyed by the canonical id of the union of every member's ids.
func foldAdvisoryRows(findings []api.SecurityFinding, rows []int) api.SecurityFinding {
	members := slices.Clone(rows)
	slices.SortFunc(members, func(a, b int) int {
		return cmp.Or(
			cmp.Compare(findings[a].Tool.DriverID, findings[b].Tool.DriverID),
			cmp.Compare(findings[a].Fingerprints.Primary, findings[b].Fingerprints.Primary),
			cmp.Compare(a, b),
		)
	})
	row := findings[members[0]]
	ensureSources(&row)
	appendSource(&row, sourceLabel(row))
	for _, other := range members[1:] {
		row = mergeSCAFindings(row, findings[other])
	}
	adv := row.Properties.Lycaon.Advisory
	advisory.Normalize(adv)
	if len(members) > 1 {
		row.RuleID = advisory.RuleID(adv)
	}
	RefreshFingerprint(&row)
	return row
}

func Advisory(f api.SecurityFinding) *api.AdvisoryRef {
	if f.Properties == nil || f.Properties.Lycaon == nil {
		return nil
	}
	return f.Properties.Lycaon.Advisory
}

// advisoryPackageKey is the package coordinate rows must share to merge, or ""
// when the row names no package: the same advisory affects several packages.
func advisoryPackageKey(f api.SecurityFinding) string {
	adv := Advisory(f)
	if adv == nil || adv.Package == nil || strings.TrimSpace(adv.Package.Name) == "" {
		return ""
	}
	return strings.TrimSpace(adv.Package.Name) + "@" +
		strings.TrimSpace(adv.Package.Version) + "#" + NormalizePackageEcosystem(adv.Package.Ecosystem)
}

// advisoryMergeKey identifies one advisory in one package after merge; it is
// what a finding group keys on.
func advisoryMergeKey(f api.SecurityFinding) string {
	pkg := advisoryPackageKey(f)
	adv := Advisory(f)
	if pkg == "" || adv == nil || strings.TrimSpace(adv.OSVID) == "" {
		return ""
	}
	return "osv:" + strings.ToUpper(strings.TrimSpace(adv.OSVID)) + "|package:" + pkg
}

func mergeSCAFindings(winner, other api.SecurityFinding) api.SecurityFinding {
	out := winner
	props := *winner.Properties
	lycaonProps := *props.Lycaon
	props.Lycaon = &lycaonProps
	out.Properties = &props
	out.Properties.Lycaon.Advisory = mergeAdvisoryMetadata(winner.Properties.Lycaon.Advisory, other.Properties.Lycaon.Advisory)
	if api.FindingLevelRank(other.Level) < api.FindingLevelRank(winner.Level) {
		out.Level = other.Level
		out.Properties.Lycaon.Advisory.SeveritySource = other.Properties.Lycaon.Advisory.SeveritySource
	} else if api.FindingLevelRank(other.Level) == api.FindingLevelRank(winner.Level) && out.Properties.Lycaon.Advisory != nil {
		otherSrc := ""
		if otherAdv := Advisory(other); otherAdv != nil {
			otherSrc = otherAdv.SeveritySource
		}
		if severitySourceRank(otherSrc) < severitySourceRank(out.Properties.Lycaon.Advisory.SeveritySource) {
			out.Properties.Lycaon.Advisory.SeveritySource = otherSrc
		}
	}
	if preferSCALocation(other, winner) {
		out.Locations = uniqueLocations(other.Locations, winner.Locations)
	} else {
		out.Locations = uniqueLocations(winner.Locations, other.Locations)
	}
	if strings.TrimSpace(other.Message) != "" && len(strings.TrimSpace(winner.Message)) < len(strings.TrimSpace(other.Message)) {
		out.Message = other.Message
	}
	ensureSources(&out)
	appendSource(&out, sourceLabel(other))
	return out
}

// uniqueLocations concatenates location lists in order, keeping the first copy
// of a location that several records cite.
func uniqueLocations(lists ...[]api.SecurityFindingLocation) []api.SecurityFindingLocation {
	seen := map[api.SecurityFindingLocation]bool{}
	var out []api.SecurityFindingLocation
	for _, list := range lists {
		for _, loc := range list {
			if !seen[loc] {
				seen[loc] = true
				out = append(out, loc)
			}
		}
	}
	return out
}

func preferSCALocation(candidate, current api.SecurityFinding) bool {
	cURI := PrimaryURI(candidate)
	curURI := PrimaryURI(current)
	if isLockfileURI(cURI) && !isLockfileURI(curURI) {
		return true
	}
	if FindingKind(candidate) == api.FindingKindContainer && FindingKind(current) != api.FindingKindContainer && !isLockfileURI(curURI) {
		return true
	}
	return false
}

func isLockfileURI(uri string) bool {
	uri = strings.ToLower(strings.TrimSpace(uri))
	if uri == "" {
		return false
	}
	base := uri
	if idx := strings.LastIndex(uri, "/"); idx >= 0 {
		base = uri[idx+1:]
	}
	switch base {
	case "go.mod", "go.sum", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "cargo.lock", "gemfile.lock", "pipfile.lock", "poetry.lock":
		return true
	default:
		return strings.HasSuffix(base, ".lock") || strings.HasSuffix(base, ".mod") || strings.HasSuffix(base, ".sum")
	}
}

func sourceLabel(f api.SecurityFinding) string {
	if f.Tool.DriverID != "" {
		return f.Tool.DriverID
	}
	return f.Tool.Name
}

func ensureSources(f *api.SecurityFinding) {
	if f.Properties == nil {
		f.Properties = &api.SecurityFindingProperties{Lycaon: &api.SecurityFindingLycaonProperties{}}
	}
	if f.Properties.Lycaon == nil {
		f.Properties.Lycaon = &api.SecurityFindingLycaonProperties{}
	}
	if f.Properties.Lycaon.Sources == nil {
		f.Properties.Lycaon.Sources = []string{}
	}
}

func appendSource(f *api.SecurityFinding, source string) {
	source = strings.TrimSpace(source)
	if source == "" {
		return
	}
	ensureSources(f)
	for _, existing := range f.Properties.Lycaon.Sources {
		if existing == source {
			return
		}
	}
	f.Properties.Lycaon.Sources = append(f.Properties.Lycaon.Sources, source)
}

func severitySourceRank(src string) int {
	switch src {
	case "osv.malicious_package":
		return 0
	case "osv.cvss":
		return 1
	case "osv.database_specific.severity":
		return 2
	case "ghsa.cvss":
		return 3
	case "nvd.cvss":
		return 4
	default:
		return 5
	}
}

