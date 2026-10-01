package findings

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/pkg/pathglob"
)

// A run's review accounts for every scanner group bound to it: a claim or
// finding assesses it, or a set-aside names it with a reason.

// SetAside accounts for scanner groups together. It names groups by id, or
// selects every group one scanner reported entirely inside path globs.
type SetAside struct {
	GroupIDs []string
	Scanner  string
	Paths    []string
	Reason   string
}

// HasSelector reports whether the set-aside selects by scanner or paths.
func (s SetAside) HasSelector() bool {
	return strings.TrimSpace(s.Scanner) != "" || len(s.Paths) > 0
}

// Selects reports whether the set-aside's selector covers a group: the named
// scanner reported it, and every place it was reported sits inside a glob.
func (s SetAside) Selects(g InventoryGroup) bool {
	if !s.HasSelector() {
		return false
	}
	if scanner := strings.TrimSpace(s.Scanner); scanner != "" && scanner != g.Scanner {
		return false
	}
	if len(s.Paths) == 0 {
		return true
	}
	if len(g.Paths) == 0 {
		return false
	}
	for _, p := range g.Paths {
		if !anyGlobCovers(s.Paths, p) {
			return false
		}
	}
	return true
}

func anyGlobCovers(globs []string, path string) bool {
	rel := pathglob.NormalizeRel(path)
	if rel == "" {
		return false
	}
	for _, g := range globs {
		if pathglob.Covers(strings.TrimSpace(g), rel) {
			return true
		}
	}
	return false
}

// InventoryGroup is one scanner group with every place it was reported.
type InventoryGroup struct {
	ID      string
	Scanner string
	RuleID  string
	Level   api.FindingLevel
	Paths   []string
}

// InventoryAccount is how a run's scanner inventory stands against its review.
type InventoryAccount struct {
	// Groups run most severe first.
	Groups []InventoryGroup
	// Linked groups are assessed by a claim or finding.
	Linked map[string]bool
	// SetAsideBy maps a group to the first set-aside that accounts for it;
	// SetAsideCounts counts the groups each set-aside accounts for.
	SetAsideBy     map[string]int
	SetAsideCounts []int
	// Unknown are cited ids that name no group in the inventory.
	Unknown []string
}

// Total is the number of groups in the inventory.
func (a InventoryAccount) Total() int { return len(a.Groups) }

// LinkedCount is the number of groups a claim or finding assessed.
func (a InventoryAccount) LinkedCount() int {
	n := 0
	for _, g := range a.Groups {
		if a.Linked[g.ID] {
			n++
		}
	}
	return n
}

// SetAsideCount is the number of groups a set-aside accounts for and no
// assessment links.
func (a InventoryAccount) SetAsideCount() int {
	n := 0
	for _, g := range a.Groups {
		if _, ok := a.SetAsideBy[g.ID]; ok && !a.Linked[g.ID] {
			n++
		}
	}
	return n
}

// Unaccounted lists the groups neither an assessment nor a set-aside covers,
// most severe first.
func (a InventoryAccount) Unaccounted() []InventoryGroup {
	var out []InventoryGroup
	for _, g := range a.Groups {
		if a.Linked[g.ID] {
			continue
		}
		if _, ok := a.SetAsideBy[g.ID]; ok {
			continue
		}
		out = append(out, g)
	}
	return out
}

// InventoryFindings are the stored findings of a run's bound scans, each
// stamped with the scanner that reported it.
func InventoryFindings(scans []api.CodeScan) []api.SecurityFinding {
	var out []api.SecurityFinding
	for _, cs := range scans {
		for _, f := range cs.Findings {
			if f.Tool.DriverID == "" && f.Tool.Name == "" {
				f.Tool.DriverID = strings.TrimSpace(cs.ScannerID)
			}
			out = append(out, f)
		}
	}
	return out
}

// InventoryGroups groups findings the way the scan plane does, keeping every
// reported place so a path selector judges the whole group.
func InventoryGroups(findings []api.SecurityFinding) []InventoryGroup {
	byID := map[string]*InventoryGroup{}
	paths := map[string]map[string]bool{}
	for _, f := range findings {
		id := FindingGroupID(f)
		g, ok := byID[id]
		if !ok {
			g = &InventoryGroup{ID: id, Scanner: sourceLabel(f), RuleID: f.RuleID, Level: f.Level}
			byID[id] = g
			paths[id] = map[string]bool{}
		}
		if api.FindingLevelRank(f.Level) < api.FindingLevelRank(g.Level) {
			g.Level = f.Level
		}
		for _, loc := range f.Locations {
			if uri := strings.TrimSpace(loc.URI); uri != "" && !paths[id][uri] {
				paths[id][uri] = true
				g.Paths = append(g.Paths, uri)
			}
		}
	}
	out := make([]InventoryGroup, 0, len(byID))
	for _, g := range byID {
		sort.Strings(g.Paths)
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := api.FindingLevelRank(out[i].Level), api.FindingLevelRank(out[j].Level); ri != rj {
			return ri < rj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// AccountInventory settles every group against the ids the review linked and
// the set-asides it declared.
func AccountInventory(groups []InventoryGroup, linked []string, setAsides []SetAside) InventoryAccount {
	out := InventoryAccount{
		Groups:         groups,
		Linked:         map[string]bool{},
		SetAsideBy:     map[string]int{},
		SetAsideCounts: make([]int, len(setAsides)),
	}
	known := make(map[string]bool, len(groups))
	for _, g := range groups {
		known[g.ID] = true
	}
	unknown := map[string]bool{}
	for _, id := range linked {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if known[id] {
			out.Linked[id] = true
		} else {
			unknown[id] = true
		}
	}
	for i, sa := range setAsides {
		named := map[string]bool{}
		for _, id := range sa.GroupIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if known[id] {
				named[id] = true
			} else {
				unknown[id] = true
			}
		}
		for _, g := range groups {
			if !named[g.ID] && !sa.Selects(g) {
				continue
			}
			out.SetAsideCounts[i]++
			if _, taken := out.SetAsideBy[g.ID]; !taken {
				out.SetAsideBy[g.ID] = i
			}
		}
	}
	for id := range unknown {
		out.Unknown = append(out.Unknown, id)
	}
	sort.Strings(out.Unknown)
	return out
}
