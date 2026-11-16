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

// LoadCoordinatorSurfaceCard returns the surface banner copy and whether the
// surface publishes text accompanying tool calls.
func LoadCoordinatorSurfaceCard(surfaceID string) (SurfaceCard, error) {
	row, err := loadCoordinatorSurfaceRow(surfaceID)
	if err != nil {
		return SurfaceCard{}, err
	}
	return SurfaceCard{
		Label:         strings.TrimSpace(row.Card.Label),
		Rule:          strings.TrimSpace(row.Card.Rule),
		PublishesText: row.PublishesMidTurnText,
	}, nil
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

// MergeCoordinatorKickVarsForTools answers tool facts from the tools the next
// model call can reach.
func MergeCoordinatorKickVarsForTools(into map[string]any, reader Reader) error {
	if into == nil {
		return fmt.Errorf("nil template vars map")
	}
	occurrence := make(map[string]any, len(into))
	for k, v := range into {
		if !IsDeclaredOrReservedReaderFact(k) {
			occurrence[k] = v
		}
	}
	merged, err := Vars(reader, occurrence)
	if err != nil {
		return err
	}
	for k, v := range merged {
		if _, exists := into[k]; !exists || !IsDeclaredOrReservedReaderFact(k) {
			into[k] = v
		}
	}
	spawn.RefreshSizingHintVars(into)
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
