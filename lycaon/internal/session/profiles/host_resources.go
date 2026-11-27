package profiles

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/flosch/pongo2/v6"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/skills"
)

const skillHostResourcesMetadata = "host_resources"

// SetHostResources wires machine-state host-resource resolution into skill availability.
func (m *Service) SetHostResources(service *hostresources.Service) {
	if m == nil {
		return
	}
	m.hostResources = service
}

func applySkillHostResources(
	ctx context.Context,
	loaded []skills.Skill,
	diags []extpacks.Diagnostic,
	snapshot hostresources.Snapshot,
	reach hostresources.Reach,
) ([]skills.Skill, []extpacks.Diagnostic) {
	out := make([]skills.Skill, 0, len(loaded))
	for _, skill := range loaded {
		raw := strings.TrimSpace(skill.Metadata[skillHostResourcesMetadata])
		if raw == "" {
			out = append(out, skill)
			continue
		}
		groups, err := hostresources.ParseRequirementGroups(raw)
		if err != nil {
			diags = append(diags, skillHostResourceDiagnostic(
				extpacks.DiagSkillHostResourcesInvalid, skill, err.Error(),
			))
			continue
		}
		all := map[string]struct{}{}
		var flat []string
		for _, group := range groups {
			for _, id := range group.Alternatives {
				if _, seen := all[id]; seen {
					continue
				}
				all[id] = struct{}{}
				flat = append(flat, id)
			}
		}
		resolved, _ := hostresources.ResolveIDs(snapshot, flat, reach.Surfaces)
		chosen := map[string]hostresources.State{}
		var unmetGroups, deniedGroups []string
		for _, group := range groups {
			met, anyResolved := false, false
			for _, id := range group.Alternatives {
				state, ok := resolved[id]
				if !ok {
					continue
				}
				anyResolved = true
				if state.Access == hostresources.AccessDeny {
					continue
				}
				chosen[id] = state
				met = true
			}
			switch {
			case met:
			case anyResolved:
				deniedGroups = append(deniedGroups, group.String())
			default:
				unmetGroups = append(unmetGroups, group.String())
			}
		}
		if len(unmetGroups) > 0 {
			diags = append(diags, skillHostResourceDiagnostic(
				extpacks.DiagSkillHostResourcesUnmet, skill,
				fmt.Sprintf("host resources unavailable: %s", strings.Join(unmetGroups, ", ")),
			))
			continue
		}
		if len(deniedGroups) > 0 {
			diags = append(diags, skillHostResourceDiagnostic(
				extpacks.DiagSkillHostResourcesUnmet, skill,
				fmt.Sprintf("host resources blocked by policy: %s", strings.Join(deniedGroups, ", ")),
			))
			continue
		}
		decorated, err := decorateSkillWithHostResources(ctx, skill, chosen, snapshot, reach)
		if err != nil {
			diags = append(diags, skillHostResourceDiagnostic(extpacks.DiagSkillHostResourcesInvalid, skill, err.Error()))
			continue
		}
		out = append(out, decorated)
	}
	return out, diags
}

func skillHostResourceDiagnostic(code string, skill skills.Skill, message string) extpacks.Diagnostic {
	return extpacks.Diagnostic{
		Code: code, Message: message, UnitID: skill.UnitID, PackID: skill.PackID,
	}
}

func skillHostResourcesTemplate() (*pongo2.Template, error) {
	data, err := config.Read(config.SkillHostResources)
	if err != nil {
		return nil, err
	}
	return pongoplain.Compile(string(data))
}

// decorateSkillWithHostResources prepends the skill's resolved host resources,
// with the uses this surface can take, to its body.
func decorateSkillWithHostResources(ctx context.Context, skill skills.Skill, resolved map[string]hostresources.State, snapshot hostresources.Snapshot, reach hostresources.Reach) (skills.Skill, error) {
	tpl, err := skillHostResourcesTemplate()
	if err != nil {
		return skill, fmt.Errorf("skill host-resource header: %w", err)
	}
	ids := make([]string, 0, len(resolved))
	for id := range resolved {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	resources := make([]map[string]any, 0, len(ids))
	var uses []map[string]any
	for _, id := range ids {
		state := resolved[id]
		modes := []string{}
		for _, connection := range state.Connections {
			modes = append(modes, string(connection.Mode))
		}
		resources = append(resources, map[string]any{
			"id": id, "status": string(state.Status), "access": string(state.Access), "routes": uniqueStrings(modes),
		})
		for _, use := range reach.MatchingUses(state, snapshot) {
			uses = append(uses, map[string]any{
				"resource": id, "label": use.Label, "capabilities": use.Capabilities,
				"alone": use.Alone, "ports": use.Ports, "through": use.Through,
			})
		}
	}
	header, err := pongoplain.Execute(ctx, tpl, map[string]any{"resources": resources, "uses": uses})
	if err != nil {
		return skill, fmt.Errorf("skill host-resource header: %w", err)
	}
	skill.Body = strings.TrimSpace(header) + "\n\n" + skill.Body
	return skill, nil
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := values[:0]
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
