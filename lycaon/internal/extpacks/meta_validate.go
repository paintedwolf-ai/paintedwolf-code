package extpacks

import (
	"context"
	"fmt"
	"strings"
)

// ApplyMetaValidate attaches meta-pack summaries, suite_conflicts, extends
// co-resolve checks, and meta_pack_ids onto a ValidateReport.
func ApplyMetaValidate(ctx context.Context, rep *ValidateReport, eff *EffectiveCatalog, packs []PackContent) error {
	if rep == nil {
		return nil
	}
	metas, discoverDiags, err := DiscoverMetaPacks()
	if err != nil {
		return err
	}
	desired := EmptyDesired()
	if eff != nil {
		desired = eff.Desired
	}
	summaries := BuildMetaPackSummaries(eff, metas, desired)
	summaries = appendExtendsCoResolve(ctx, summaries, metas, packs)
	rep.MetaPacks = summaries
	rep.SuiteConflicts = CountSuiteConflictMetas(summaries)
	rep.Packs = AttachMetaPackIDs(rep.Packs, metas)
	rep.Diagnostics = append(rep.Diagnostics, discoverDiags...)
	// Suite findings live on their summary for Den, and also join the report's
	// diagnostics so the one severity rule decides the verdict for them too.
	for _, s := range summaries {
		rep.Diagnostics = append(rep.Diagnostics, s.Diagnostics...)
	}
	return nil
}

// appendExtendsCoResolve checks each suite extension edge.
func appendExtendsCoResolve(ctx context.Context, summaries []MetaPackSummary, metas []MetaPack, packs []PackContent) []MetaPackSummary {
	byID := map[string]MetaPack{}
	for _, m := range metas {
		byID[m.Manifest.ID] = m
	}
	idx := map[string]int{}
	for i := range summaries {
		idx[summaries[i].ID] = i
	}
	for _, m := range metas {
		if len(m.Manifest.Extends) == 0 {
			continue
		}
		i := idx[m.Manifest.ID]
		parents := make([]string, 0, len(m.Manifest.Extends))
		cycle := false
		for _, parentID := range m.Manifest.Extends {
			parent, ok := byID[parentID]
			if !ok {
				continue // extends_unresolved already attached in BuildMetaPackSummaries
			}
			if extendsCycle(m.Manifest.ID, parentID, byID, map[string]struct{}{}) {
				cycle = true
				summaries[i].Diagnostics = append(summaries[i].Diagnostics, Diagnostic{
					Code: DiagMetaInvalid, PackID: parentID,
					Message: fmt.Sprintf("extends cycle involving %q and %q", m.Manifest.ID, parentID),
				})
				if j, ok := idx[parentID]; ok && !hasDiagCode(summaries[j].Diagnostics, DiagMetaInvalid, m.Manifest.ID) {
					summaries[j].Diagnostics = append(summaries[j].Diagnostics, Diagnostic{
						Code: DiagMetaInvalid, PackID: m.Manifest.ID,
						Message: fmt.Sprintf("extends cycle involving %q and %q", parentID, m.Manifest.ID),
					})
				}
				continue
			}
			parents = append(parents, parent.Manifest.ID)
		}
		if cycle || len(parents) == 0 {
			continue
		}
		memberSet := map[string]struct{}{}
		for _, id := range m.Manifest.Members {
			memberSet[id] = struct{}{}
		}
		for _, parentID := range parents {
			for _, id := range byID[parentID].Manifest.Members {
				memberSet[id] = struct{}{}
			}
		}
		filtered := filterPackContents(packs, memberSet)
		if len(filtered) == 0 {
			continue
		}
		desired := desiredEnableOnly(memberSet, packs)
		eff := Resolve(ctx, ResolveInput{
			Packs:   filtered,
			Desired: desired,
		})
		var conflictUnits []string
		for _, u := range eff.Units {
			if u.Status == UnitStatusConflict {
				conflictUnits = append(conflictUnits, u.ID)
			}
		}
		if len(conflictUnits) == 0 {
			continue
		}
		sortStrings(conflictUnits)
		summaries[i].Diagnostics = append(summaries[i].Diagnostics, Diagnostic{
			Code: DiagExtendsCoResolve, PackID: m.Manifest.ID,
			Message: fmt.Sprintf("extends co-resolve unit conflict: %s", strings.Join(conflictUnits, ", ")),
		})
	}
	return summaries
}

func extendsCycle(start, next string, byID map[string]MetaPack, visiting map[string]struct{}) bool {
	if next == start {
		return true
	}
	if _, ok := visiting[next]; ok {
		return false
	}
	visiting[next] = struct{}{}
	m, ok := byID[next]
	if !ok {
		return false
	}
	for _, p := range m.Manifest.Extends {
		if extendsCycle(start, p, byID, visiting) {
			return true
		}
	}
	return false
}

func filterPackContents(packs []PackContent, keep map[string]struct{}) []PackContent {
	var out []PackContent
	for _, pc := range packs {
		if _, ok := keep[pc.Pack.ID]; ok {
			out = append(out, pc)
		}
	}
	return out
}

// desiredEnableOnly enables packs in keep and disables every other known pack.
func desiredEnableOnly(keep map[string]struct{}, packs []PackContent) DesiredState {
	tr := true
	f := false
	d := EmptyDesired()
	for _, pc := range packs {
		id := pc.Pack.ID
		if _, ok := keep[id]; ok {
			d.Packs = append(d.Packs, DesiredPack{ID: id, Enabled: &tr})
		} else {
			d.Packs = append(d.Packs, DesiredPack{ID: id, Enabled: &f})
		}
	}
	return d
}

func sortStrings(in []string) {
	for i := 0; i < len(in); i++ {
		for j := i + 1; j < len(in); j++ {
			if in[j] < in[i] {
				in[i], in[j] = in[j], in[i]
			}
		}
	}
}
