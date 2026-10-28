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
		r.BindSessionProject(rootSessionID, project)
	}
	return r.FileValues(project)
}
