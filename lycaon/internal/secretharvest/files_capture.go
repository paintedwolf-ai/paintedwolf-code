package secretharvest

import (
	"errors"
	"maps"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// FileCapture stages one coherent discovery before persistence and publication.
type FileCapture struct {
	runtime   *Runtime
	project   string
	updates   map[fileIdentity]map[string]Value
	bytes     int
	err       error
	committed bool
}

func (r *Runtime) CaptureFiles(project string) *FileCapture {
	return &FileCapture{runtime: r, project: project, updates: map[fileIdentity]map[string]Value{}}
}

func (c *FileCapture) Observe(file FileContent) error {
	if c.committed {
		return errors.New("credential-file capture already committed")
	}
	if c.err != nil {
		return c.err
	}
	if c.project == "" || file.ProjectID != c.project || file.RootID == "" || file.Path == "" {
		c.err = errors.New("invalid credential-file capture location")
		return c.err
	}
	current := map[string]Value{}
	for _, pair := range ParseContainer(file.Path, file.Content) {
		fp := c.runtime.fp.Fingerprint(pair.Value)
		if file.Held != nil && file.Held(fp) {
			continue
		}
		current[pair.Value] = c.runtime.fileValue(storedBinding{Name: pair.Name, Container: file.Path, Secret: pair.Value})
	}
	key := fileIdentity{file.ProjectID, file.RootID, file.Path}
	size := c.bytes
	if _, exists := c.updates[key]; !exists {
		size += len(file.RootID) + len(file.Path)
	}
	for _, value := range c.updates[key] {
		size -= len(value.Name) + len(value.Container) + len(value.secret)
	}
	for _, value := range current {
		size += len(value.Name) + len(value.Container) + len(value.secret)
	}
	if size > credentialstore.MaxPlaintextBytes {
		c.err = errors.New("credential-file capture exceeds the vault capacity")
		return c.err
	}
	c.bytes = size
	c.updates[key] = current
	return nil
}

func (r *Runtime) fileValue(binding storedBinding) Value {
	return Value{Name: binding.Name, Container: binding.Container, Fingerprint: r.fp.Fingerprint(binding.Secret), RuleID: secretmatch.HarvestRuleID, Title: "A value from " + binding.Container, Source: secretmatch.SourceContainerHarvest, secret: binding.Secret}
}

func (c *FileCapture) Commit() error {
	if c.committed {
		return nil
	}
	if c.err != nil {
		return c.err
	}
	if len(c.updates) == 0 {
		c.committed = true
		return nil
	}
	r := c.runtime
	r.mu.Lock()
	retired := maps.Clone(r.retiredFiles[c.project])
	if retired == nil {
		retired = map[string]Value{}
	}
	files := map[fileIdentity]map[string]Value{}
	for key, values := range r.byFile {
		if key.project == c.project {
			files[key] = values
		}
	}
	changed := false
	for key, current := range c.updates {
		previous := files[key]
		for secret, old := range previous {
			if _, remains := current[secret]; !remains {
				old.Retired = true
				retired[secret] = old
				changed = true
			}
		}
		for secret := range current {
			if _, exists := previous[secret]; !exists {
				changed = true
			}
		}
		if len(current) == 0 {
			delete(files, key)
		} else {
			files[key] = current
		}
	}
	if err := r.persistFilesLocked(c.project, files, retired); err != nil {
		r.mu.Unlock()
		return err
	}
	for key := range r.byFile {
		if key.project == c.project {
			delete(r.byFile, key)
		}
	}
	for key, values := range files {
		r.byFile[key] = values
	}
	r.retiredFiles[c.project] = retired
	var roots []string
	if changed {
		for root, project := range r.projects {
			if project == c.project {
				roots = append(roots, root)
			}
		}
	}
	c.committed = true
	r.mu.Unlock()
	for _, root := range roots {
		r.Invalidate(root)
	}
	return nil
}
