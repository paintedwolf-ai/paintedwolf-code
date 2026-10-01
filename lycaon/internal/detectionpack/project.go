package detectionpack

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"gopkg.in/yaml.v3"
)

// The project tier may only enable installed packs. A clone arrives with its
// overlay already in the tree, so disabling from there would let unreviewed
// content decide which rules never fire; enabling only adds asks.

// Reject codes for project overlay rows.
const (
	RejectProjectUnknownID       = "project_unknown_id"
	RejectProjectFieldsForbidden = "project_fields_forbidden"
	RejectProjectUnreadable      = "project_unreadable"
	RejectDuplicateID            = "duplicate_id"
	RejectInvalidEntry           = "invalid_entry"
)

// RejectedRow is one refused project overlay row.
type RejectedRow struct {
	ID     string
	Code   string
	Detail string
}

// projectRowFields are the only keys a project row may carry.
var projectRowFields = map[string]bool{"id": true, "enabled": true}

// ProjectPacksRelPath is relative to the project root.
func ProjectPacksRelPath() string { return settingsoverlay.Rel("detection-packs.yaml") }

// ProjectPacksPath returns {projectDir}/<overlay>/detection-packs.yaml.
func ProjectPacksPath(projectDir string) string {
	return filepath.Join(projectDir, ProjectPacksRelPath())
}

// projectPackRow is one accepted row: a pack this project asks for. There is no
// enabled field because there is only one answer a project may give.
type projectPackRow struct {
	id string
}

// loadProjectPackRows reads the project overlay. Missing files are empty.
func loadProjectPackRows(projectDir string) ([]projectPackRow, []RejectedRow) {
	path := ProjectPacksPath(projectDir)
	data, err := os.ReadFile(path) // #nosec G304 -- project overlay under the caller's project dir
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []RejectedRow{{
			Code:   RejectProjectUnreadable,
			Detail: fmt.Sprintf("could not read %s: %v", ProjectPacksRelPath(), err),
		}}
	}
	// Rows decode loosely so an unknown key refuses that row rather than the file.
	var raw struct {
		Packs []map[string]any `yaml:"packs"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, []RejectedRow{{
			Code:   RejectProjectUnreadable,
			Detail: fmt.Sprintf("could not parse %s: %v", ProjectPacksRelPath(), err),
		}}
	}

	var rows []projectPackRow
	var rejected []RejectedRow
	seen := map[string]struct{}{}
	for _, entry := range raw.Packs {
		id := ""
		if v, ok := entry["id"]; ok && v != nil {
			id = strings.TrimSpace(fmt.Sprint(v))
		}
		var extra []string
		for key := range entry {
			if !projectRowFields[strings.TrimSpace(key)] {
				extra = append(extra, key)
			}
		}
		switch {
		case len(extra) > 0:
			sort.Strings(extra)
			rejected = append(rejected, RejectedRow{
				ID:   id,
				Code: RejectProjectFieldsForbidden,
				Detail: "a project row may only set id and enabled; saw " +
					strings.Join(extra, ", "),
			})
		case id == "":
			rejected = append(rejected, RejectedRow{Code: RejectInvalidEntry, Detail: "row is missing id"})
		default:
			if _, dup := seen[id]; dup {
				rejected = append(rejected, RejectedRow{ID: id, Code: RejectDuplicateID, Detail: "id appears more than once"})
				continue
			}
			seen[id] = struct{}{}
			enabled, ok := entry["enabled"].(bool)
			if !ok {
				rejected = append(rejected, RejectedRow{
					ID:     id,
					Code:   RejectInvalidEntry,
					Detail: "enabled must be true",
				})
				continue
			}
			if !enabled {
				rejected = append(rejected, RejectedRow{
					ID:   id,
					Code: RejectInvalidEntry,
					Detail: "a project may only turn a pack on; turning one off is a " +
						"device decision, made in Settings",
				})
				continue
			}
			rows = append(rows, projectPackRow{id: id})
		}
	}
	return rows, rejected
}

// applyProjectEnableOverlay turns on the packs the project names. An id the
// device does not have is refused rather than created, and no row can turn one
// off, so this tier only ever adds asks to what the device already hears.
func applyProjectEnableOverlay(packs []Pack, rows []projectPackRow) []RejectedRow {
	byID := make(map[string]int, len(packs))
	for i := range packs {
		byID[packs[i].ID] = i
	}
	var rejected []RejectedRow
	for _, row := range rows {
		idx, ok := byID[row.id]
		if !ok {
			rejected = append(rejected, RejectedRow{
				ID:     row.id,
				Code:   RejectProjectUnknownID,
				Detail: "no detection pack with this id is installed on this device",
			})
			continue
		}
		packs[idx].Enabled = true
	}
	return rejected
}
