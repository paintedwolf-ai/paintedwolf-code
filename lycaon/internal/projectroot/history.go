package projectroot

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/fspath"
)

// historyLimit bounds the retired roots one project remembers.
const historyLimit = 64

// Retirement says why an attached root no longer answers for its old spelling.
type Retirement string

const (
	// RootDetached means the folder was removed from the project.
	RootDetached Retirement = "removed"
	// RootMoved means the same folder is attached at another path.
	RootMoved Retirement = "moved"
	// RootRenamed means the same folder now carries another label.
	RootRenamed Retirement = "renamed"
)

// Retired is one root spelling a project change retired. Successor is the
// same root after a move or rename; a removed root has none.
type Retired struct {
	Root       RootRef
	Reason     Retirement
	Successor  RootRef
	Generation int
}

// RootChange is one advance of a project's attachment generation, as the
// difference between the roots before and after it.
type RootChange struct {
	Generation int
	Added      []RootRef
	Retired    []Retired
	// Primary is the new primary root when a kept root took that role.
	Primary RootRef
}

// Empty reports a change with nothing to announce.
func (c RootChange) Empty() bool {
	return len(c.Added) == 0 && len(c.Retired) == 0 && c.Primary.ID == ""
}

// History is the engine's record of root attachment changes per project.
// Attachment handlers record every change; tool contexts capture the
// generation they were built at, so later effects and stale spellings are
// decided from these facts rather than from path text. Nothing persists.
type History struct {
	mu       sync.Mutex
	projects map[string]*projectHistory
}

type projectHistory struct {
	generation int
	retired    []Retired
}

// HistoryView is one project's generation and retired spellings at capture.
type HistoryView struct {
	history    *History
	projectID  string
	Generation int
}

// Record advances projectID's generation when before and after differ and
// returns the change; an unchanged set returns false. A nil History reports
// the change without recording it.
func (h *History) Record(projectID string, before, after []RootRef) (RootChange, bool) {
	change := DiffRoots(before, after)
	changed := !change.Empty()
	if h == nil || strings.TrimSpace(projectID) == "" || !changed {
		return change, changed
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.projects == nil {
		h.projects = map[string]*projectHistory{}
	}
	project := h.projects[projectID]
	if project == nil {
		project = &projectHistory{}
		h.projects[projectID] = project
	}
	project.generation++
	change.Generation = project.generation
	for i := range change.Retired {
		change.Retired[i].Generation = project.generation
	}
	project.retired = append(project.retired, change.Retired...)
	if extra := len(project.retired) - historyLimit; extra > 0 {
		project.retired = append([]Retired(nil), project.retired[extra:]...)
	}
	return change, true
}

// View captures projectID's current generation.
func (h *History) View(projectID string) HistoryView {
	view := HistoryView{history: h, projectID: projectID}
	if h == nil {
		return view
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if project := h.projects[projectID]; project != nil {
		view.Generation = project.generation
	}
	return view
}

// retired returns the retirements recorded after generation since.
func (v HistoryView) retired(since int) []Retired {
	if v.history == nil {
		return nil
	}
	v.history.mu.Lock()
	defer v.history.mu.Unlock()
	project := v.history.projects[v.projectID]
	if project == nil {
		return nil
	}
	var out []Retired
	for _, r := range project.retired {
		if r.Generation > since {
			out = append(out, r)
		}
	}
	return out
}

// StaleLabel reports the latest retirement of a label no current root
// carries. Labels compare case-insensitively, as attached labels do.
func (v HistoryView) StaleLabel(current []RootRef, label string) (Retired, bool) {
	if _, err := rootByLabel(current, label); err == nil {
		return Retired{}, false
	}
	retired := v.retired(0)
	for i := len(retired) - 1; i >= 0; i-- {
		if strings.EqualFold(retired[i].Root.Label, label) {
			return retired[i], true
		}
	}
	return Retired{}, false
}

// StalePath reports the latest retirement of a root path that contains abs
// when no current root does. Paths compare in canonical form.
func (v HistoryView) StalePath(current []RootRef, abs string) (Retired, bool) {
	if !filepath.IsAbs(abs) {
		return Retired{}, false
	}
	if _, _, err := rootForAbsolute(current, filepath.Clean(abs)); err == nil {
		return Retired{}, false
	}
	canonical := fspath.CanonicalPath(abs)
	retired := v.retired(0)
	for i := len(retired) - 1; i >= 0; i-- {
		r := retired[i]
		if r.Reason == RootRenamed {
			continue
		}
		if under(filepath.Clean(abs), filepath.Clean(r.Root.Path)) || under(canonical, fspath.CanonicalPath(r.Root.Path)) {
			return r, true
		}
	}
	return Retired{}, false
}

// RetiredSince reports a removal or move of root recorded after this view's
// generation: effects under its old path no longer have attachment authority.
// A rename keeps the path, so it never retires effect authority.
func (v HistoryView) RetiredSince(root RootRef) (Retired, bool) {
	for _, r := range v.retired(v.Generation) {
		if r.Reason != RootRenamed && r.Root.ID == root.ID && filepath.Clean(r.Root.Path) == filepath.Clean(root.Path) {
			return r, true
		}
	}
	return Retired{}, false
}

func under(path, root string) bool {
	if path == "" || root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// DiffRoots compares root sets by identity: a new ID is added, a missing ID
// is removed, and a kept ID with another path or label is moved or renamed.
func DiffRoots(before, after []RootRef) RootChange {
	var change RootChange
	previous := make(map[string]RootRef, len(before))
	for _, r := range before {
		previous[r.ID] = r
	}
	kept := make(map[string]bool, len(after))
	for _, r := range after {
		old, ok := previous[r.ID]
		kept[r.ID] = ok
		switch {
		case !ok:
			change.Added = append(change.Added, r)
		case filepath.Clean(old.Path) != filepath.Clean(r.Path):
			change.Retired = append(change.Retired, Retired{Root: old, Reason: RootMoved, Successor: r})
		case old.Label != r.Label:
			change.Retired = append(change.Retired, Retired{Root: old, Reason: RootRenamed, Successor: r})
		}
	}
	for _, r := range before {
		if !kept[r.ID] {
			change.Retired = append(change.Retired, Retired{Root: r, Reason: RootDetached})
		}
	}
	oldPrimary, oldErr := PrimaryRoot(before)
	newPrimary, newErr := PrimaryRoot(after)
	if oldErr == nil && newErr == nil && oldPrimary.ID != newPrimary.ID {
		change.Primary = newPrimary
	}
	return change
}
