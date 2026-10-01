package extpacks

import (
	"fmt"
	"sort"
	"strings"
)

// SuiteStatus treats absent enablement as enabled.
func SuiteStatus(members []string, present map[string]bool, enabled map[string]bool) (MetaPackStatus, []Diagnostic) {
	var diags []Diagnostic
	enabledCount := 0
	disabledCount := 0
	missingCount := 0
	for _, id := range members {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if !present[id] {
			missingCount++
			diags = append(diags, Diagnostic{
				Code: DiagMemberMissing, PackID: id,
				Message: fmt.Sprintf("member pack %q is missing from inventory", id),
			})
			continue
		}
		isEnabled := true
		if v, ok := enabled[id]; ok {
			isEnabled = v
		}
		if isEnabled {
			enabledCount++
			continue
		}
		disabledCount++
		diags = append(diags, Diagnostic{
			Code: DiagMemberDisabled, PackID: id,
			Message: fmt.Sprintf("member pack %q is disabled", id),
		})
	}
	switch {
	case enabledCount > 0 && missingCount == 0 && disabledCount == 0:
		return MetaPackComplete, diags
	case enabledCount == 0:
		return MetaPackInactive, diags
	default:
		return MetaPackPartial, diags
	}
}

func BuildMetaPackSummaries(eff *EffectiveCatalog, metas []MetaPack, desired DesiredState) []MetaPackSummary {
	present := map[string]bool{}
	enabled := map[string]bool{}
	if eff != nil {
		for _, p := range eff.Packs {
			present[p.ID] = true
			enabled[p.ID] = p.Enabled
		}
	}
	for _, row := range desired.Packs {
		if _, ok := enabled[row.ID]; !ok {
			enabled[row.ID] = PackEnabled(desired, row.ID)
		}
	}

	byID := map[string]MetaPack{}
	summaries := make([]MetaPackSummary, 0, len(metas))
	for _, m := range metas {
		byID[m.Manifest.ID] = m
		status, diags := SuiteStatus(m.Manifest.Members, present, enabledMapFor(desired, m.Manifest.Members, enabled))
		if status == MetaPackInactive {
			filtered := diags[:0]
			for _, d := range diags {
				if d.Code == DiagMemberDisabled {
					continue
				}
				filtered = append(filtered, d)
			}
			diags = filtered
		}
		for _, parent := range m.Manifest.Extends {
			if _, ok := byID[parent]; !ok && !metaIDIn(metas, parent) {
				diags = append(diags, Diagnostic{
					Code: DiagExtendsUnresolved, PackID: parent,
					Message: fmt.Sprintf("extends %q is not discovered", parent),
				})
			}
		}
		name := strings.TrimSpace(m.Manifest.Name)
		if name == "" {
			name = m.Manifest.ID
		}
		summaries = append(summaries, MetaPackSummary{
			ID:            m.Manifest.ID,
			Name:          name,
			Version:       m.Manifest.Version,
			Kind:          m.Kind,
			Status:        status,
			Members:       append([]string{}, m.Manifest.Members...),
			ConflictsWith: append([]string{}, m.Manifest.ConflictsWith...),
			Extends:       append([]string{}, m.Manifest.Extends...),
			Diagnostics:   diags,
			Removable:     !IsStockMetaPackID(m.Manifest.ID),
		})
	}

	attachSuiteConflicts(summaries, metas, enabledMapAll(desired, present, enabled))
	sort.SliceStable(summaries, func(i, j int) bool {
		if IsStockMetaPackID(summaries[i].ID) {
			return !IsStockMetaPackID(summaries[j].ID)
		}
		if IsStockMetaPackID(summaries[j].ID) {
			return false
		}
		return summaries[i].ID < summaries[j].ID
	})
	return summaries
}

func enabledMapFor(desired DesiredState, members []string, known map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, id := range members {
		if v, ok := known[id]; ok {
			out[id] = v
			continue
		}
		out[id] = PackEnabled(desired, id)
	}
	return out
}

func enabledMapAll(desired DesiredState, present, known map[string]bool) map[string]bool {
	out := map[string]bool{}
	for id := range present {
		if v, ok := known[id]; ok {
			out[id] = v
		} else {
			out[id] = PackEnabled(desired, id)
		}
	}
	for _, row := range desired.Packs {
		if _, ok := out[row.ID]; !ok {
			out[row.ID] = PackEnabled(desired, row.ID)
		}
	}
	return out
}

func metaIDIn(metas []MetaPack, id string) bool {
	for _, m := range metas {
		if m.Manifest.ID == id {
			return true
		}
	}
	return false
}

func enabledMemberCountDefaultTrue(members []string, present, enabled map[string]bool) int {
	n := 0
	for _, id := range members {
		if !present[id] {
			continue
		}
		if v, ok := enabled[id]; ok {
			if v {
				n++
			}
			continue
		}
		n++
	}
	return n
}

func attachSuiteConflicts(summaries []MetaPackSummary, metas []MetaPack, enabled map[string]bool) {
	present := map[string]bool{}
	for id := range enabled {
		present[id] = true
	}
	byID := map[string]*MetaPackSummary{}
	for i := range summaries {
		byID[summaries[i].ID] = &summaries[i]
	}
	manByID := map[string]MetaPackManifest{}
	for _, m := range metas {
		manByID[m.Manifest.ID] = m.Manifest
	}
	for i := range summaries {
		a := &summaries[i]
		manA := manByID[a.ID]
		countA := enabledMemberCountDefaultTrue(manA.Members, presentFromSummaryEnabled(enabled), enabled)
		if countA < 1 {
			continue
		}
		for _, otherID := range conflictPeers(manA, manByID) {
			b := byID[otherID]
			if b == nil {
				continue
			}
			manB := manByID[otherID]
			countB := enabledMemberCountDefaultTrue(manB.Members, presentFromSummaryEnabled(enabled), enabled)
			if countB < 1 {
				continue
			}
			if hasDiagCode(a.Diagnostics, DiagSuiteConflict, otherID) {
				continue
			}
			msg := fmt.Sprintf("suite conflict between %q and %q", a.Name, b.Name)
			a.Diagnostics = append(a.Diagnostics, Diagnostic{
				Code: DiagSuiteConflict, PackID: otherID, Message: msg,
			})
			if !hasDiagCode(b.Diagnostics, DiagSuiteConflict, a.ID) {
				b.Diagnostics = append(b.Diagnostics, Diagnostic{
					Code: DiagSuiteConflict, PackID: a.ID, Message: msg,
				})
			}
		}
	}
}

func presentFromSummaryEnabled(enabled map[string]bool) map[string]bool {
	out := map[string]bool{}
	for id := range enabled {
		out[id] = true
	}
	return out
}

func conflictPeers(man MetaPackManifest, all map[string]MetaPackManifest) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(id string) {
		if id == man.ID {
			return
		}
		if _, ok := all[id]; !ok {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, id := range man.ConflictsWith {
		add(id)
	}
	for id, other := range all {
		for _, c := range other.ConflictsWith {
			if c == man.ID {
				add(id)
			}
		}
	}
	sort.Strings(out)
	return out
}

func hasDiagCode(diags []Diagnostic, code, packID string) bool {
	for _, d := range diags {
		if d.Code == code && d.PackID == packID {
			return true
		}
	}
	return false
}

// AttachMetaPackIDs sets PackSummary.MetaPackIDs from discovered suite membership.
func AttachMetaPackIDs(packs []PackSummary, metas []MetaPack) []PackSummary {
	if len(packs) == 0 {
		return packs
	}
	membership := map[string][]string{}
	for _, m := range metas {
		for _, member := range m.Manifest.Members {
			membership[member] = append(membership[member], m.Manifest.ID)
		}
	}
	for member, ids := range membership {
		sort.Strings(ids)
		membership[member] = ids
	}
	out := append([]PackSummary(nil), packs...)
	for i := range out {
		if ids := membership[out[i].ID]; len(ids) > 0 {
			out[i].MetaPackIDs = append([]string(nil), ids...)
		}
	}
	return out
}

// CountSuiteConflictMetas returns how many meta-pack summaries carry suite_conflict.
func CountSuiteConflictMetas(summaries []MetaPackSummary) int {
	n := 0
	for _, s := range summaries {
		for _, d := range s.Diagnostics {
			if d.Code == DiagSuiteConflict {
				n++
				break
			}
		}
	}
	return n
}
