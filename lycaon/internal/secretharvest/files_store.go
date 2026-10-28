package secretharvest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

type storedBinding struct {
	Name      string `json:"name"`
	Container string `json:"container"`
	Secret    string `json:"secret"`
}
type storedProject struct {
	Format int             `json:"format"`
	Values []storedBinding `json:"values"`
}

// RestoreFiles restores retained bytes; discovery establishes current bindings.
func (r *Runtime) RestoreFiles(store *credentialstore.Store) error {
	projects := map[string]map[string]Value{}
	for _, project := range store.IDs() {
		raw, _ := store.Get(project)
		var doc storedProject
		decoder := json.NewDecoder(bytes.NewBufferString(raw.Value()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&doc); err != nil {
			return fmt.Errorf("credential-file evidence for project %s is invalid", project)
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) || doc.Format != 1 {
			return fmt.Errorf("credential-file evidence for project %s has an unsupported format", project)
		}
		values := map[string]Value{}
		for _, binding := range doc.Values {
			if binding.Secret == "" || binding.Container == "" {
				return errors.New("invalid credential-file binding")
			}
			values[binding.Secret] = Value{Name: binding.Name, Container: binding.Container, Fingerprint: r.fp.Fingerprint(binding.Secret), RuleID: secretmatch.HarvestRuleID, Title: "A value from " + binding.Container, Source: secretmatch.SourceContainerHarvest, secret: binding.Secret, Retired: true}
		}
		projects[project] = values
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retiredFiles = projects
	r.fileStore = store
	return nil
}

func (r *Runtime) persistFileLocked(key fileIdentity, current map[string]Value) error {
	if r.fileStore == nil {
		return nil
	}
	unique := map[string]Value{}
	for _, value := range r.fileValuesLocked(key.project) {
		unique[value.secret] = value
	}
	for secret, value := range current {
		unique[secret] = value
	}
	doc := storedProject{Format: 1, Values: make([]storedBinding, 0, len(unique))}
	for _, value := range unique {
		doc.Values = append(doc.Values, storedBinding{Name: value.Name, Container: value.Container, Secret: value.secret})
	}
	slices.SortFunc(doc.Values, func(a, b storedBinding) int { return strings.Compare(a.Secret, b.Secret) })
	raw, err := json.Marshal(doc) // #nosec G117 -- The credential store encrypts this payload.
	if err != nil {
		return err
	}
	previous, ok := r.fileStore.Get(key.project)
	if ok && previous.Value() == string(raw) {
		return nil
	}
	return r.fileStore.Set(key.project, string(raw))
}

// ForgetProject removes evidence only after explicit project deletion.
func (r *Runtime) ForgetProject(project string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fileStore != nil {
		if err := r.fileStore.Delete(project); err != nil {
			return err
		}
	}
	delete(r.retiredFiles, project)
	for key := range r.byFile {
		if key.project == project {
			delete(r.byFile, key)
		}
	}
	return nil
}
