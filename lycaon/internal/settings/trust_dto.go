package settings

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TrustSettingsToDTO(s *TrustSurfacesStore) wire.TrustSettingsResponse {
	if s == nil {
		return wire.TrustSettingsResponse{}
	}
	rows := projectcontrib.Registry()
	out := wire.TrustSettingsResponse{
		Surfaces: make([]wire.TrustSurfaceSetting, 0, len(rows)),
	}
	for _, row := range rows {
		out.Surfaces = append(out.Surfaces, wire.TrustSurfaceSetting{
			ID:      wire.TrustSurfaceId(row.ID),
			Label:   row.Label,
			Group:   wire.TrustSurfaceGroup(row.Group),
			Enabled: s.DeviceEnabled(row.ID),
		})
	}
	return out
}

// ProjectTrustToDTO joins scanned content with project trust state.
func ProjectTrustToDTO(
	s *TrustSurfacesStore,
	p project.Project,
	scan projectcontrib.Manifest,
) wire.ProjectTrust {
	if s == nil {
		return wire.ProjectTrust{}
	}
	rows := projectcontrib.Registry()
	rootIDs := make(map[string]string, len(p.Roots))
	for _, root := range p.Roots {
		if path := strings.TrimSpace(root.Path); path != "" {
			rootIDs[path] = root.ID
		}
	}
	out := wire.ProjectTrust{
		ProjectID: p.ID,
		Surfaces:  make([]wire.ProjectTrustSurface, 0, len(rows)),
	}
	read := SeenRecordsFor(p, scan)
	out.Review, out.UnreadCount = TrustComparison(p, read)
	for _, row := range rows {
		found, _ := scan.Surface(row.ID)
		count := found.Count
		deviceOn := s.DeviceEnabled(row.ID)
		projectOn := project.TrustEnabled(p, row.ID)
		seen := project.SurfaceSeen(p, row.ID, read[row.ID].Stamp)

		surface := wire.ProjectTrustSurface{
			ID:             wire.TrustSurfaceId(row.ID),
			Label:          row.Label,
			Group:          wire.TrustSurfaceGroup(row.Group),
			Count:          count,
			Items:          toTrustItems(found.Items, rootIDs),
			DeviceEnabled:  deviceOn,
			ProjectEnabled: projectOn,
			Applying:       deviceOn && projectOn,
			Seen:           seen,
		}
		out.Surfaces = append(out.Surfaces, surface)
	}
	return out
}

func toTrustItems(in []projectcontrib.Item, rootIDs map[string]string) []wire.TrustSurfaceItem {
	out := make([]wire.TrustSurfaceItem, 0, len(in))
	for _, item := range in {
		out = append(out, wire.TrustSurfaceItem{
			Name:   item.Name,
			Detail: item.Detail,
			RootID: rootIDs[item.RootPath],
			Path:   item.Path,
			Lines:  item.Lines,
		})
	}
	return out
}

func ValidateTrustSwitchIds(ids map[string]bool) error {
	for id := range ids {
		if _, ok := projectcontrib.Lookup(id); !ok {
			return fmt.Errorf("trust surface %q cannot be switched", id)
		}
	}
	return nil
}
