// Package projectremoval owns reviewed project deletion and optional device cleanup.
package projectremoval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type evidence struct {
	Checks     []wire.ProjectRemovalCheck
	References map[string][]string
	Complete   bool
	Revision   string
}

func digest(parts ...string) string {
	var encoded []byte
	for _, part := range parts {
		encoded = strconv.AppendInt(encoded, int64(len(part)), 10)
		encoded = append(encoded, ':')
		encoded = append(encoded, part...)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (o *Owner) referenceEvidence(ctx context.Context, removedID string) (evidence, error) {
	projects, err := o.Projects.List(ctx)
	if err != nil {
		return evidence{}, err
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].ID < projects[j].ID })
	out := evidence{Checks: []wire.ProjectRemovalCheck{}, References: map[string][]string{}, Complete: true}
	identities := []string{}
	for _, p := range projects {
		if p.ID == removedID {
			continue
		}
		if err := ctx.Err(); err != nil {
			return evidence{}, err
		}
		check, suggestions := o.checkProject(p)
		out.Checks = append(out.Checks, check)
		identities = append(identities, p.ID, strconv.Itoa(p.RootsGeneration), check.Name, check.State, check.Revision, check.Reason, strconv.Itoa(len(p.Roots)))
		for _, root := range p.Roots {
			identities = append(identities, root.ID, root.Path, string(root.Kind))
		}
		if check.State != "checked" {
			out.Complete = false
		}
		seen := map[string]bool{}
		for _, row := range suggestions.Suggest {
			if !seen[row.ID] {
				out.References[row.ID] = append(out.References[row.ID], p.ID)
				seen[row.ID] = true
			}
		}
	}
	out.Revision = digest(identities...)
	return out, nil
}

func (o *Owner) checkProject(p project.Project) (wire.ProjectRemovalCheck, extpacks.SuggestionManifest) {
	check := wire.ProjectRemovalCheck{ProjectID: p.ID, Name: p.Name, State: "unknown"}
	empty := extpacks.EmptySuggestion()
	if len(p.Roots) == 0 {
		check.State, check.Revision = "checked", "no_roots"
		return check, empty
	}
	if o.SuggestionsApply == nil || !o.SuggestionsApply(p) {
		check.Reason = "suggestions_disabled"
		return check, empty
	}
	combined := empty
	revisions := []string{}
	check.State = "checked"
	for _, root := range p.Roots {
		source, manifest := checkRoot(p, root)
		revisions = append(revisions, digest(source.ProjectID, source.State, source.Revision, source.Reason))
		combined.Suggest = append(combined.Suggest, manifest.Suggest...)
		if source.State != "checked" {
			check.State = "unknown"
			if check.Reason == "" {
				check.Reason = source.Reason
			}
		}
	}
	check.Revision = digest(revisions...)
	return check, combined
}

func checkRoot(p project.Project, root project.Root) (wire.ProjectRemovalCheck, extpacks.SuggestionManifest) {
	check := wire.ProjectRemovalCheck{ProjectID: p.ID, State: "unknown", Revision: root.ID}
	empty := extpacks.EmptySuggestion()
	overlay, err := project.ResolveProjectOverlay(&p, root.ID)
	if err != nil {
		check.Reason = "overlay_unavailable"
		return check, empty
	}
	if err = overlay.CheckCompatibility(); err != nil {
		check.Reason = "overlay_incompatible"
		return check, empty
	}
	// Missing roots leave reference evidence unknown.
	info, err := os.Stat(root.Path)
	if err != nil || !info.IsDir() {
		check.Reason = "root_unavailable"
		return check, empty
	}
	path := extpacks.ProjectDesiredPath(root.Path)
	data, err := os.ReadFile(path)
	if err != nil && (!os.IsNotExist(err) || !manifestAbsent(path)) {
		check.Reason = "manifest_unreadable"
		return check, empty
	}
	manifest := empty
	if len(bytes.TrimSpace(data)) > 0 {
		manifest, err = extpacks.ParseSuggestion(path, data)
		if err != nil {
			check.Reason = "manifest_invalid"
			check.Revision = digest(string(data))
			return check, empty
		}
	}
	check.State, check.Revision = "checked", digest(string(data))
	return check, manifest
}

func manifestAbsent(path string) bool {
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return false
	}
	parent := filepath.Dir(path)
	if info, err := os.Stat(parent); err == nil {
		return info.IsDir()
	}
	// A dangling configuration symlink leaves its contents unknown.
	_, err := os.Lstat(parent)
	return os.IsNotExist(err)
}

func (o *Owner) Assess(ctx context.Context, projectID string) (wire.ProjectRemovalAssessment, error) {
	if _, err := o.Projects.Get(ctx, projectID); err != nil {
		return wire.ProjectRemovalAssessment{}, err
	}
	return o.assess(ctx, projectID)
}

func (o *Owner) assess(ctx context.Context, projectID string) (wire.ProjectRemovalAssessment, error) {
	refs, err := o.referenceEvidence(ctx, projectID)
	if err != nil {
		return wire.ProjectRemovalAssessment{}, err
	}
	if o.Extensions == nil {
		return wire.ProjectRemovalAssessment{}, fmt.Errorf("extension assessment unavailable")
	}
	state, err := o.Extensions.RemovalState()
	if err != nil {
		return wire.ProjectRemovalAssessment{}, err
	}
	out := wire.ProjectRemovalAssessment{ProjectID: projectID, ExtensionRevision: state.Revision, Complete: refs.Complete, Checks: refs.Checks, Extensions: []wire.ProjectRemovalExtension{}}
	for _, pack := range state.Desired.Packs {
		if pack.InstalledFrom != projectID {
			continue
		}
		out.Extensions = append(out.Extensions, assessPack(pack.ID, state, refs))
	}
	sort.Slice(out.Extensions, func(i, j int) bool { return out.Extensions[i].PackID < out.Extensions[j].PackID })
	out.AssessmentToken = digest(projectID, state.Revision, refs.Revision)
	return out, nil
}

func assessPack(id string, state extensionstate.RemovalState, refs evidence) wire.ProjectRemovalExtension {
	row := wire.ProjectRemovalExtension{PackID: id, Disposition: "eligible", Reasons: []wire.ProjectRemovalReason{}}
	for _, projectID := range refs.References[id] {
		row.Reasons = append(row.Reasons, wire.ProjectRemovalReason{Code: "project_suggestion", SubjectID: projectID})
	}
	for _, pack := range state.Lock.Packages {
		if _, required := pack.Dependencies[id]; required {
			row.Reasons = append(row.Reasons, wire.ProjectRemovalReason{Code: "extension_dependency", SubjectID: pack.ID})
		}
	}
	for unit, pack := range state.Desired.Own {
		if pack == id {
			row.Reasons = append(row.Reasons, wire.ProjectRemovalReason{Code: "device_selection", SubjectID: unit})
		}
	}
	if len(state.Desired.Configuration[id]) > 0 {
		row.Reasons = append(row.Reasons, wire.ProjectRemovalReason{Code: "device_configuration"})
	}
	if extpacks.IsStockPackID(id) {
		row.Reasons = append(row.Reasons, wire.ProjectRemovalReason{Code: "stock_extension"})
	}
	sort.Slice(row.Reasons, func(i, j int) bool {
		a, b := row.Reasons[i], row.Reasons[j]
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.SubjectID < b.SubjectID
	})
	if len(row.Reasons) > 0 {
		row.Disposition = "retained"
	} else if !refs.Complete {
		row.Disposition = "unknown"
		row.Reasons = append(row.Reasons, wire.ProjectRemovalReason{Code: "incomplete_project_evidence"})
	}
	return row
}
