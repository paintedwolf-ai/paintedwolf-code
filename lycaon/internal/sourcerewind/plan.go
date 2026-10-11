// Package sourcerewind restores a selected source-history suffix through the
// shared mutation journal, retaining exact provenance and recovery state.
package sourcerewind

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/textfile"
)

type Documents interface {
	RecoverSourceRewind(context.Context, *project.Project, []sourceledger.RewindFile, func() error) error
	RollbackSourceRewind(context.Context, *project.Project, []sourceledger.RewindFile, func() error) error
	ResolveSourceRewind(context.Context, *project.Project, string, []string, *sourceledger.RewindPlan) error
	CheckSourceRewind(context.Context, *project.Project, []sourceledger.RewindFile) ([]sourceledger.RewindIssue, error)
	WithSourceRewind(context.Context, *project.Project, []sourceledger.RewindFile, func() error) error
}

type Planner interface {
	PlanRewind(context.Context, string, string, []string) (sourceledger.RewindPlan, error)
}

type Service struct {
	Planner   Planner
	Mutations sourceeffect.Journal
	Documents Documents
}

// Operation is persisted by the session transaction before the first file
// effect. Contents remain available for recovery even if blob retention runs.
type Operation struct {
	AttemptID   string `json:"attempt_id"`
	ProjectID   string `json:"project_id"`
	WorkspaceID string `json:"workspace_id"`
	PersonID    string `json:"person_id"`
	Files       []File `json:"files"`
	Applied     int    `json:"applied"`
	Pending     bool   `json:"pending"`
	Phase       string `json:"phase"`
}

type File struct {
	sourceledger.RewindFile
	RootPath string
	Mode     uint32
}

func (s *Service) Prepare(ctx context.Context, p *project.Project, sessionID string, anchors []string) (*Operation, error) {
	if s == nil || s.Planner == nil || s.Mutations == nil {
		return nil, fmt.Errorf("source rewind service unavailable")
	}
	plan, err := s.Planner.PlanRewind(ctx, p.ID, sessionID, anchors)
	if err != nil {
		return nil, err
	}
	if s.Documents != nil {
		if err := s.Documents.ResolveSourceRewind(ctx, p, sessionID, anchors, &plan); err != nil {
			return nil, err
		}
	}
	op := &Operation{AttemptID: uuid.NewString(), ProjectID: p.ID, WorkspaceID: p.WorkspaceID(), Phase: "prepared", Files: []File{}}
	for _, file := range plan.Files {
		if file.Target.State == "absent" {
			file.Target.Path = file.Expected.Path
			file.Target.RootID = file.Expected.RootID
		}
		if file.Expected.State == "absent" {
			file.Expected.Path = file.Target.Path
			file.Expected.RootID = file.Target.RootID
		}
		issue := func(code string) {
			plan.Issues = append(plan.Issues, sourceledger.RewindIssue{RootID: file.Expected.RootID, Path: file.Expected.Path, Code: code})
		}
		root := ""
		for _, r := range p.Roots {
			if r.ID == file.Expected.RootID {
				root = r.Path
				break
			}
		}
		if root == "" || file.Expected.RootID != file.Target.RootID || p.BranchForRoot(file.Expected.RootID) != file.BranchID {
			issue("workspace_unavailable")
			continue
		}
		f := File{RewindFile: file, RootPath: root, Mode: 0o644}
		if err := matchState(root, file.Expected); err != nil {
			issue("working_file_changed")
			continue
		}
		if file.Expected.State == "content" {
			info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(file.Expected.Path)))
			if err != nil {
				return nil, err
			}
			f.Mode = uint32(info.Mode().Perm())
		}
		if file.Expected.Path != file.Target.Path && file.Target.State != "absent" {
			absent := file.Target
			absent.State = "absent"
			absent.SHA256 = ""
			if err := matchState(root, absent); err != nil {
				issue("destination_occupied")
				continue
			}
		}
		if sameState(file.Expected, file.Target) {
			continue
		}
		op.Files = append(op.Files, f)
	}
	if len(plan.Issues) > 0 {
		return nil, &sourceledger.RewindBlockedError{Issues: plan.Issues}
	}
	return op, nil
}

func (o *Operation) Paths() []string {
	paths := []string{}
	seen := map[string]bool{}
	for _, f := range o.Files {
		for _, p := range []string{f.Expected.Path, f.Target.Path} {
			if !seen[p] {
				paths = append(paths, p)
				seen[p] = true
			}
		}
	}
	return paths
}

func (o *Operation) Selection() []sourceledger.RewindFile {
	files := make([]sourceledger.RewindFile, 0, len(o.Files))
	for _, f := range o.Files {
		selection := f.RewindFile
		selection.ActorPersonID = o.PersonID
		files = append(files, selection)
	}
	return files
}

func sameState(a, b sourceledger.RestorableVersion) bool {
	return a.State == b.State && a.SHA256 == b.SHA256 && a.Path == b.Path && a.RootID == b.RootID
}

// Recovery resolves the same logical roots and branch before touching disk.
func (o *Operation) ValidateWorkspace(p *project.Project) error {
	if p == nil || p.ID != o.ProjectID || p.WorkspaceID() != o.WorkspaceID || o.Applied < 0 || o.Applied > len(o.Files) {
		return fmt.Errorf("invalid rewind recovery identity")
	}
	if _, err := uuid.Parse(o.AttemptID); err != nil {
		return fmt.Errorf("invalid rewind recovery attempt")
	}
	switch o.Phase {
	case "prepared", "rolled_back":
		if o.Applied != 0 || o.Pending {
			return fmt.Errorf("invalid rewind preparation cursor")
		}
	case "files_applied":
		if o.Applied != len(o.Files) || o.Pending {
			return fmt.Errorf("invalid rewind completion cursor")
		}
	case "applying", "rolling_back":
		if o.Pending && o.Applied == len(o.Files) {
			return fmt.Errorf("invalid pending rewind cursor")
		}
	default:
		return fmt.Errorf("unknown rewind phase %q", o.Phase)
	}
	for _, file := range o.Files {
		if err := file.validate(); err != nil {
			return err
		}
		found := false
		for _, root := range p.Roots {
			if root.ID == file.Expected.RootID && filepath.Clean(root.Path) == filepath.Clean(file.RootPath) && p.BranchForRoot(root.ID) == file.BranchID {
				found = true
				break
			}
		}
		if !found {
			return &sourceledger.RewindBlockedError{Issues: []sourceledger.RewindIssue{{RootID: file.Expected.RootID, Path: file.Expected.Path, Code: "workspace_unavailable"}}}
		}
	}
	return nil
}

func (f File) validate() error {
	if f.FileID == "" || f.Expected.RootID != f.Target.RootID || f.Mode > 0o777 {
		return fmt.Errorf("invalid rewind file identity")
	}
	for _, v := range []sourceledger.RestorableVersion{f.Expected, f.Target} {
		if _, err := location(f.RootPath, v.Path); err != nil {
			return err
		}
		switch v.State {
		case "content":
			if textfile.SHA256(v.Content) != v.SHA256 {
				return fmt.Errorf("rewind content integrity mismatch")
			}
		case "absent":
			if len(v.Content) != 0 || v.SHA256 != "" {
				return fmt.Errorf("invalid absent rewind state")
			}
		default:
			return fmt.Errorf("unsupported rewind state %q", v.State)
		}
	}
	return nil
}
