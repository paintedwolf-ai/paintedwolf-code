package prompts

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
)

const (
	// CoordinatorProfileID is the bundled coordinator tool profile id.
	CoordinatorProfileID = "coordinator"
	coordinatorReadScope = "coordinator_product_read"
)

type CoordinatorPathScopeData struct {
	ReadScopeName  string
	ReadGlobs      []string
	WriteGlobs     []string
	WriteDenyGlobs []string
}

// LoadInvestigatePathScopes loads product read/write scopes for implement_investigate.
func LoadInvestigatePathScopes() (CoordinatorPathScopeData, error) {
	reg, err := sandbox.LoadPathScopes()
	if err != nil {
		return CoordinatorPathScopeData{}, err
	}
	readScope, ok := reg["coordinator_product_read"]
	if !ok {
		return CoordinatorPathScopeData{}, fmt.Errorf("missing path scope coordinator_product_read")
	}
	writeScope, ok := reg["coordinator_product_write"]
	if !ok {
		return CoordinatorPathScopeData{}, fmt.Errorf("missing path scope %q", "coordinator_product_write")
	}
	readGlobs := append([]string(nil), readScope.Read...)
	sort.Strings(readGlobs)
	writeGlobs := append([]string(nil), writeScope.Write...)
	sort.Strings(writeGlobs)
	denyGlobs := append([]string(nil), writeScope.Deny...)
	sort.Strings(denyGlobs)
	return CoordinatorPathScopeData{
		ReadScopeName:  "coordinator_product_read",
		ReadGlobs:      readGlobs,
		WriteGlobs:     writeGlobs,
		WriteDenyGlobs: denyGlobs,
	}, nil
}

// LoadCoordinatorPathScopes loads the bundled coordinator profile path scopes.
func LoadCoordinatorPathScopes() (CoordinatorPathScopeData, error) {
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		return CoordinatorPathScopeData{}, err
	}
	return CoordinatorPathScopesFromProfiles(profiles)
}

// CoordinatorPathScopesFromProfiles finds the coordinator profile in profiles.
func CoordinatorPathScopesFromProfiles(profiles []sandbox.ToolProfile) (CoordinatorPathScopeData, error) {
	for _, p := range profiles {
		if p.ID != CoordinatorProfileID {
			continue
		}
		return coordinatorPathScopesFromProfile(p), nil
	}
	return CoordinatorPathScopeData{}, fmt.Errorf("tool profile %q not found", CoordinatorProfileID)
}

func coordinatorPathScopesFromProfile(p sandbox.ToolProfile) CoordinatorPathScopeData {
	readGlobs := append([]string(nil), p.ReadGlobs...)
	sort.Strings(readGlobs)
	writeGlobs := append([]string(nil), p.WriteGlobs...)
	sort.Strings(writeGlobs)
	return CoordinatorPathScopeData{
		ReadScopeName: coordinatorReadScope,
		ReadGlobs:     readGlobs,
		WriteGlobs:    writeGlobs,
	}
}

// CoordinatorPathScopeTemplateVars builds path-scope template data.
func CoordinatorPathScopeTemplateVars(data CoordinatorPathScopeData) map[string]any {
	return map[string]any{
		"read_scope_name":  data.ReadScopeName,
		"read_globs":       data.ReadGlobs,
		"write_globs":      data.WriteGlobs,
		"write_deny_globs": data.WriteDenyGlobs,
	}
}

// CoordinatorProfileVisibleTools returns sorted coordinator tools.
func CoordinatorProfileVisibleTools() ([]string, error) {
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		return nil, err
	}
	return CoordinatorProfileVisibleToolsFromProfiles(profiles)
}

// CoordinatorProfileVisibleToolsFromProfiles finds enabled tools on the coordinator profile.
func CoordinatorProfileVisibleToolsFromProfiles(profiles []sandbox.ToolProfile) ([]string, error) {
	for _, p := range profiles {
		if p.ID != CoordinatorProfileID {
			continue
		}
		var out []string
		for name, enabled := range p.Tools {
			if enabled {
				out = append(out, name)
			}
		}
		sort.Strings(out)
		return out, nil
	}
	return nil, fmt.Errorf("tool profile %q not found", CoordinatorProfileID)
}

// LoadCoordinatorSurfaceCard returns the surface banner copy.
func LoadCoordinatorSurfaceCard(surfaceID string) (label, rule string, err error) {
	row, err := loadCoordinatorSurfaceRow(surfaceID)
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(row.Card.Label), strings.TrimSpace(row.Card.Rule), nil
}

func loadCoordinatorSurfaceRow(surfaceID string) (surfacecatalog.Surface, error) {
	surfaceID = strings.TrimSpace(surfaceID)
	if surfaceID == "" {
		return surfacecatalog.Surface{}, fmt.Errorf("empty surface id")
	}
	catalog, err := surfacecatalog.Load()
	if err != nil {
		return surfacecatalog.Surface{}, err
	}
	return catalog.Surface(surfaceID)
}

// LoadCoordinatorSurfaceFloor returns the tools a surface offers on every call.
func LoadCoordinatorSurfaceFloor(surfaceID string) ([]string, error) {
	row, err := loadCoordinatorSurfaceRow(surfaceID)
	if err != nil {
		return nil, err
	}
	out := append([]string(nil), row.Floor...)
	sort.Strings(out)
	return out, nil
}

// LoadCoordinatorSurfaceLoadable returns the tools a surface can load or request.
func LoadCoordinatorSurfaceLoadable(surfaceID string) ([]string, error) {
	row, err := loadCoordinatorSurfaceRow(surfaceID)
	if err != nil {
		return nil, err
	}
	out := row.LoadableTools()
	sort.Strings(out)
	return out, nil
}

// MergeCoordinatorTurnSurfaceVars adds turn-scoped flags for conditional coordinator mode partials.
func MergeCoordinatorTurnSurfaceVars(surfaceID string, pendingOverlayPromote bool, into map[string]any) {
	if into == nil {
		return
	}
	into["surface_id"] = strings.TrimSpace(surfaceID)
	into["pending_overlay_promote"] = pendingOverlayPromote
}

// MergeCoordinatorKickPolicyVars adds coordinator kick policy variables.
func MergeCoordinatorKickPolicyVars(into map[string]any) error {
	if into == nil {
		return fmt.Errorf("nil template vars map")
	}
	visible, err := CoordinatorProfileVisibleTools()
	if err != nil {
		return err
	}
	for k, v := range CoordinatorPolicyTemplateVars(visible, nil) {
		if _, exists := into[k]; !exists {
			into[k] = v
		}
	}
	spawn.RefreshSizingHintVars(into)
	return nil
}

// MergeCoordinatorSurfacePathVars adds path and policy variables. The floor
// plus the tools turn.Loaded names are offered on this call and teach their
// copy; the rest of the loadable set lists as requestable, each with the
// description turn.Schemas holds for it, and loads through request_tools.
func MergeCoordinatorSurfacePathVars(surfaceID string, profiles []sandbox.ToolProfile, into map[string]any, turn SurfaceTurn) error {
	if into == nil {
		return fmt.Errorf("nil template vars map")
	}
	var data CoordinatorPathScopeData
	var err error
	if strings.TrimSpace(surfaceID) == "implement_investigate" {
		// Product path scopes are host YAML, not catalog units.
		data, err = LoadInvestigatePathScopes()
	} else if len(profiles) == 0 {
		data, err = LoadCoordinatorPathScopes()
	} else {
		data, err = CoordinatorPathScopesFromProfiles(profiles)
	}
	if err != nil {
		return err
	}
	for k, v := range CoordinatorPathScopeTemplateVars(data) {
		into[k] = v
	}
	floor, err := LoadCoordinatorSurfaceFloor(surfaceID)
	if err != nil {
		return err
	}
	loadable, err := LoadCoordinatorSurfaceLoadable(surfaceID)
	if err != nil {
		return err
	}
	offered, requestable := OfferedTools(floor, loadable, turn.Loaded)
	for k, v := range CoordinatorPolicyTemplateVars(offered, requestable) {
		into[k] = v
	}
	roster, err := RequestableTools(requestable, turn.Schemas)
	if err != nil {
		return err
	}
	into["surface_offered"] = offered
	into["requestable_tools"] = requestableToolVars(roster)
	into["requestable_capabilities"] = RequestableCapabilities(roster)
	clearSkillRosterWithoutSkillsRead(into)
	return nil
}

// OfferedTools partitions a surface's loadable tools by the turn's loaded set:
// offered is the floor plus every loaded loadable tool, requestable the rest.
func OfferedTools(floor, loadable []string, loaded map[string]bool) (offered, requestable []string) {
	offered = append([]string(nil), floor...)
	seen := make(map[string]bool, len(floor)+len(loadable))
	for _, name := range floor {
		seen[name] = true
	}
	for _, name := range loadable {
		if seen[name] {
			continue
		}
		seen[name] = true
		if loaded[name] {
			offered = append(offered, name)
		} else {
			requestable = append(requestable, name)
		}
	}
	sort.Strings(offered)
	sort.Strings(requestable)
	return offered, requestable
}

// clearSkillRosterWithoutSkillsRead drops the catalog when the surface lacks skills_read.
func clearSkillRosterWithoutSkillsRead(into map[string]any) {
	if readable, _ := into["profile_has_skills_read"].(bool); readable {
		return
	}
	delete(into, "agent_skills")
}
