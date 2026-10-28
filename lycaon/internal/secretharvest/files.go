package secretharvest

import (
	"strings"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

type fileIdentity struct{ project, root, path string }

type FileContent struct {
	ProjectID, RootID, Path string
	Content                 []byte
	Held                    func(secretmatch.SecretFingerprint) bool
}

// ObserveFile replaces a path's active bindings and retains bytes that left it.
func (r *Runtime) ObserveFile(file FileContent) {
	if r == nil || r.fp == nil || file.ProjectID == "" || file.RootID == "" || file.Path == "" {
		return
	}
	current := map[string]Value{}
	for _, pair := range ParseContainer(file.Path, file.Content) {
		fp := r.fp.Fingerprint(pair.Value)
		if file.Held != nil && file.Held(fp) {
			continue
		}
		current[pair.Value] = Value{Name: pair.Name, Container: file.Path, Fingerprint: fp, RuleID: secretmatch.HarvestRuleID, Title: "A value from " + file.Path, Source: secretmatch.SourceContainerHarvest, secret: pair.Value}
	}
	r.mu.Lock()
	key := fileIdentity{file.ProjectID, file.RootID, file.Path}
	previous := r.byFile[key]
	if r.byFile == nil {
		r.byFile = map[fileIdentity]map[string]Value{}
	}
	if r.retiredFiles == nil {
		r.retiredFiles = map[string]map[string]Value{}
	}
	if r.retiredFiles[file.ProjectID] == nil {
		r.retiredFiles[file.ProjectID] = map[string]Value{}
	}
	changed := false
	for secret, old := range previous {
		if _, remains := current[secret]; !remains {
			old.Retired = true
			r.retiredFiles[file.ProjectID][secret] = old
			changed = true
		}
	}
	for secret := range current {
		if _, exists := previous[secret]; !exists {
			changed = true
		}
	}
	if len(current) == 0 {
		delete(r.byFile, key)
	} else {
		r.byFile[key] = current
	}
	var roots []string
	if changed {
		for root, project := range r.projects {
			if project == file.ProjectID {
				roots = append(roots, root)
			}
		}
	}
	r.mu.Unlock()
	for _, root := range roots {
		r.Invalidate(root)
	}
}

func (r *Runtime) FileValues(projectID string) []Value {
	if r == nil || strings.TrimSpace(projectID) == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.fileValuesLocked(projectID)
}

func (r *Runtime) fileValuesLocked(projectID string) []Value {
	unique := map[string]Value{}
	for project, values := range r.retiredFiles {
		if projectID != "" && project != projectID {
			continue
		}
		for secret, value := range values {
			unique[secret] = value
		}
	}
	for file, values := range r.byFile {
		if projectID != "" && file.project != projectID {
			continue
		}
		for secret, value := range values {
			unique[secret] = value
		}
	}
	out := make([]Value, 0, len(unique))
	for _, value := range unique {
		out = append(out, value)
	}
	return out
}

func (r *Runtime) HasFileValue(projectID string, fp secretmatch.SecretFingerprint) bool {
	for _, value := range r.FileValues(projectID) {
		if value.Fingerprint == fp {
			return true
		}
	}
	return false
}

func (r *Runtime) FileValuesForSession(rootSessionID string) []Value {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	project, resolve := r.projects[rootSessionID], r.projectOf
	r.mu.RUnlock()
	if project == "" && resolve != nil {
		project = resolve(rootSessionID)
		r.bindProject(rootSessionID, project)
	}
	return r.FileValues(project)
}
