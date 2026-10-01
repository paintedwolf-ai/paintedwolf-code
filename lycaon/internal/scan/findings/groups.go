package findings

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/lycaon/lycaon/pkg/api"
)

// FindingGroup is an inventory entry, not a reviewed vulnerability. Counts cover
// all matching stored findings; locations are a bounded, deterministic sample.
type FindingGroup struct {
	ID            string                 `json:"id"`
	Kind          api.FindingKind        `json:"kind"`
	RuleID        string                 `json:"rule_id"`
	Scanner       string                 `json:"scanner"`
	Level         api.FindingLevel       `json:"level"`
	Count         int                    `json:"count"`
	Message       string                 `json:"message"`
	Advisory      *api.AdvisoryRef       `json:"advisory,omitempty"`
	Locations     []FindingGroupLocation `json:"locations"`
	LocationCount int                    `json:"location_count"`
}

// FindingGroupLocation pairs a representative location with its scanner message.
type FindingGroupLocation struct {
	api.SecurityFindingLocation
	Message string `json:"message"`
}

// FindingGroupID keeps distinct packages and scanner rules separate. It is
// independent of input order, severity, scan id, and the location sample.
func FindingGroupID(f api.SecurityFinding) string {
	key := advisoryMergeKey(f)
	if key == "" {
		kind, source := string(FindingKind(f)), sourceLabel(f)
		key = fmt.Sprintf("%d:%s%d:%s%d:%s", len(kind), kind, len(source), source, len(f.RuleID), f.RuleID)
	}
	hash := sha256.Sum256([]byte(key))
	return "group:" + hex.EncodeToString(hash[:16])
}

// GroupFindings groups before applying any presentation budget.
func GroupFindings(findings []api.SecurityFinding, maxLocations int) []FindingGroup {
	groups := map[string]*FindingGroup{}
	locations := map[string]map[api.SecurityFindingLocation]string{}
	for _, f := range findings {
		id := FindingGroupID(f)
		g := groups[id]
		if g == nil {
			g = &FindingGroup{ID: id, Kind: FindingKind(f), RuleID: f.RuleID, Scanner: sourceLabel(f), Level: f.Level, Message: f.Message}
			if f.Properties != nil && f.Properties.Lycaon != nil {
				g.Advisory = f.Properties.Lycaon.Advisory
			}
			groups[id] = g
			locations[id] = map[api.SecurityFindingLocation]string{}
		}
		g.Count++
		source := ""
		if g.Advisory != nil {
			source = g.Advisory.SeveritySource
		}
		if f.Properties != nil && f.Properties.Lycaon != nil {
			g.Advisory = mergeAdvisoryMetadata(g.Advisory, f.Properties.Lycaon.Advisory)
			if candidate := f.Properties.Lycaon.Advisory; candidate != nil {
				if api.FindingLevelRank(f.Level) < api.FindingLevelRank(g.Level) || (f.Level == g.Level && (source == "" || candidate.SeveritySource != "" && candidate.SeveritySource < source)) {
					source = candidate.SeveritySource
				}
			}
		}
		if g.Advisory != nil {
			g.Advisory.SeveritySource = source
		}
		if api.FindingLevelRank(f.Level) < api.FindingLevelRank(g.Level) {
			g.Level = f.Level
		}
		g.Scanner = min(g.Scanner, sourceLabel(f))
		g.RuleID = min(g.RuleID, f.RuleID)
		g.Kind = min(g.Kind, FindingKind(f))
		if f.Message != g.Message {
			g.Message = ""
		}
		for _, loc := range f.Locations {
			if prior, ok := locations[id][loc]; !ok || f.Message < prior {
				locations[id][loc] = f.Message
			}
		}
	}
	out := make([]FindingGroup, 0, len(groups))
	for id, g := range groups {
		for loc, message := range locations[id] {
			g.Locations = append(g.Locations, FindingGroupLocation{SecurityFindingLocation: loc, Message: message})
		}
		sort.Slice(g.Locations, func(i, j int) bool {
			a, b := g.Locations[i], g.Locations[j]
			if a.URI != b.URI {
				return a.URI < b.URI
			}
			if a.StartLine != b.StartLine {
				return a.StartLine < b.StartLine
			}
			if a.StartColumn != b.StartColumn {
				return a.StartColumn < b.StartColumn
			}
			if a.EndLine != b.EndLine {
				return a.EndLine < b.EndLine
			}
			return a.EndColumn < b.EndColumn
		})
		g.LocationCount = len(g.Locations)
		if maxLocations > 0 && len(g.Locations) > maxLocations {
			g.Locations = g.Locations[:maxLocations]
		}
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if api.FindingLevelRank(out[i].Level) != api.FindingLevelRank(out[j].Level) {
			return api.FindingLevelRank(out[i].Level) < api.FindingLevelRank(out[j].Level)
		}
		return out[i].ID < out[j].ID
	})
	return out
}
