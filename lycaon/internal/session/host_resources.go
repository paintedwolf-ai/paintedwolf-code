package session

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/skills"
)

const skillHostResourcesMetadata = "host_resources"

// SetHostResources wires machine-state host-resource resolution into skill availability.
func (m *Manager) SetHostResources(service *hostresources.Service) {
	if m == nil {
		return
	}
	m.hostResources = service
}

func applySkillHostResources(
	loaded []skills.Skill,
	diags []extpacks.Diagnostic,
	snapshot hostresources.Snapshot,
	surfaces []hostresources.ExecutionSurface,
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
		resolved, _ := hostresources.ResolveIDs(snapshot, flat, surfaces)
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
		out = append(out, decorateSkillWithHostResources(skill, chosen))
	}
	return out, diags
}

func skillHostResourceDiagnostic(code string, skill skills.Skill, message string) extpacks.Diagnostic {
	return extpacks.Diagnostic{
		Code: code, Message: message, UnitID: skill.UnitID, PackID: skill.PackID,
	}
}

func decorateSkillWithHostResources(skill skills.Skill, resolved map[string]hostresources.State) skills.Skill {
	ids := make([]string, 0, len(resolved))
	for id := range resolved {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var context strings.Builder
	context.WriteString("Host resource context (host-observed machine state; not authority):\n")
	for _, id := range ids {
		state := resolved[id]
		fmt.Fprintf(&context, "- id=%s status=%s access=%s", id, state.Status, state.Access)
		modes := []string{}
		for _, connection := range state.Connections {
			modes = append(modes, string(connection.Mode))
		}
		modes = uniqueStrings(modes)
		if len(modes) > 0 {
			fmt.Fprintf(&context, " routes=%s", strings.Join(modes, ","))
		}
		context.WriteByte('\n')
	}
	context.WriteString("On a process start that needs one, declare its id in capability_request.host_resources. An ask uses the unified Approvals flow; a grant may suppress repeats. Sandbox and route-specific review still apply.")
	skill.Body = context.String() + "\n\n" + skill.Body
	return skill
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
