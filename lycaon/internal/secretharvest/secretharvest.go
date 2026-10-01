// Package secretharvest holds protected values for exact-match screening.
package secretharvest

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

// MaxValuesPerContainer bounds one container's contribution.
const MaxValuesPerContainer = 256

// Value is one value held for its provenance.
type Value struct {
	// Name identifies the original binding.
	Name string
	// Container identifies the protected source.
	Container string
	// Fingerprint is the device-keyed identity shared with the shape lens.
	Fingerprint secretmatch.SecretFingerprint
	// RuleID and Title keep the identity the value was first recognized under.
	RuleID string
	Title  string
	// Source names which lens established the value as a secret.
	Source secretmatch.RedactionSource
	// Reference is the managed capability that can replace the raw value.
	Reference string
	// Retired marks managed bytes whose capability no longer resolves.
	Retired bool
	// NonDisclosable prevents this exact value from crossing the model boundary.
	NonDisclosable bool
	secret         string
}

// Secret returns the raw value for exact matching.
func (v Value) Secret() string { return v.secret }

// Runtime holds harvested values per session tree.
type Runtime struct {
	mu sync.RWMutex
	// Eviction would remove active screening evidence.
	bySession map[string]map[string]Value // root session → secret → Value
	// projects caches each session tree's project; a tree never changes project.
	projects  map[string]string
	projectOf func(rootSessionID string) string
	fp        *secretmatch.Fingerprinter
	// generation increases when a tree gains evidence.
	generation map[string]uint64
	observers  []func(rootSessionID string, generation uint64)
}

// NewRuntime returns an empty harvest store.
func NewRuntime(fp *secretmatch.Fingerprinter) *Runtime {
	return &Runtime{
		bySession:  map[string]map[string]Value{},
		projects:   map[string]string{},
		generation: map[string]uint64{},
		fp:         fp,
	}
}

// SetProjectResolver names the project a session tree belongs to, so a
// project-scoped screen reads only that project's evidence.
func (r *Runtime) SetProjectResolver(projectOf func(rootSessionID string) string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.projectOf = projectOf
}

// OnGrowth registers an unlocked evidence-growth callback.
func (r *Runtime) OnGrowth(fn func(rootSessionID string, generation uint64)) {
	if r == nil || fn == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observers = append(r.observers, fn)
}

// Invalidate advances the same evidence clock when an external exact-match
// source changes, without copying project-wide credentials into every chat.
func (r *Runtime) Invalidate(rootSessionID string) {
	if r == nil || strings.TrimSpace(rootSessionID) == "" {
		return
	}
	r.mu.Lock()
	r.generation[rootSessionID]++
	generation := r.generation[rootSessionID]
	observers := append([]func(string, uint64){}, r.observers...)
	r.mu.Unlock()
	for _, observe := range observers {
		observe(rootSessionID, generation)
	}
}

// ContainerRead is the exact content one read delivered from a credential file.
type ContainerRead struct {
	RootSessionID, ProjectID string
	// Container names the file as the read addressed it.
	Container string
	Content   []byte
	// Held reports bytes already accounted for: model-authored or managed.
	Held func(secretmatch.SecretFingerprint) bool
}

// Harvest records the bindings of a credential file read that no stronger
// evidence already accounts for.
func (r *Runtime) Harvest(read ContainerRead) []Value {
	if r == nil || strings.TrimSpace(read.RootSessionID) == "" {
		return nil
	}
	pairs := ParseContainer(read.Container, read.Content)
	if len(pairs) == 0 {
		return nil
	}
	r.bindProject(read.RootSessionID, read.ProjectID)
	values := make([]Value, 0, len(pairs))
	for _, p := range pairs {
		if read.Held != nil && r.fp != nil && read.Held(r.fp.Fingerprint(p.Value)) {
			continue
		}
		values = append(values, Value{
			Name:      p.Name,
			Container: read.Container,
			RuleID:    secretmatch.HarvestRuleID,
			Title:     "A value from " + read.Container,
			Source:    secretmatch.SourceContainerHarvest,
			secret:    p.Value,
		})
	}
	return r.add(read.RootSessionID, values)
}

func (r *Runtime) bindProject(rootSessionID, projectID string) {
	if strings.TrimSpace(projectID) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.projects[rootSessionID] = projectID
}

// Remember adds confirmed values to exact-match screening.
func (r *Runtime) Remember(rootSessionID string, items ...secretmatch.Remembered) []Value {
	if r == nil || strings.TrimSpace(rootSessionID) == "" || len(items) == 0 {
		return nil
	}
	values := make([]Value, 0, len(items))
	for _, item := range items {
		if utf8.RuneCountInString(item.Secret) < secretmatch.MinNeedleRunes(item.NonDisclosable) {
			// Values below the match floor add no screening evidence.
			continue
		}
		source := item.Source
		if source == "" {
			source = secretmatch.SourceRememberedMatch
		}
		values = append(values, Value{
			Name:           item.Name,
			Container:      item.Origin,
			RuleID:         item.RuleID,
			Title:          item.Title,
			Source:         source,
			Reference:      item.Reference,
			Retired:        item.Retired,
			NonDisclosable: item.NonDisclosable,
			secret:         item.Secret,
		})
	}
	return r.add(rootSessionID, values)
}

// stronger reports whether held evidence outranks a candidate for the same
// bytes: protected over disclosable, live over retired, referenced over bare.
func stronger(held, candidate Value) bool {
	if held.NonDisclosable != candidate.NonDisclosable {
		return held.NonDisclosable
	}
	if held.Retired != candidate.Retired {
		return candidate.Retired
	}
	return held.Reference != "" && candidate.Reference == ""
}

// add advances the generation only for new evidence.
func (r *Runtime) add(rootSessionID string, values []Value) []Value {
	if len(values) == 0 {
		return nil
	}
	r.mu.Lock()
	sess := r.bySession[rootSessionID]
	if sess == nil {
		sess = map[string]Value{}
		r.bySession[rootSessionID] = sess
	}
	out := make([]Value, 0, len(values))
	added := 0
	for _, v := range values {
		if r.fp != nil {
			v.Fingerprint = r.fp.Fingerprint(v.secret)
		}
		held, known := sess[v.secret]
		if !known {
			added++
		} else if stronger(held, v) {
			out = append(out, held)
			continue
		}
		sess[v.secret] = v
		out = append(out, v)
	}
	generation := r.generation[rootSessionID]
	observers := make([]func(string, uint64), len(r.observers))
	copy(observers, r.observers)
	if added > 0 {
		generation++
		r.generation[rootSessionID] = generation
	}
	r.mu.Unlock()

	if added > 0 {
		for _, observe := range observers {
			observe(rootSessionID, generation)
		}
	}
	return out
}

// Has reports whether the session tree harvested this fingerprint.
func (r *Runtime) Has(rootSessionID string, fp secretmatch.SecretFingerprint) bool {
	if r == nil || fp == "" || strings.TrimSpace(rootSessionID) == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.bySession[rootSessionID] {
		if v.Fingerprint == fp {
			return true
		}
	}
	return false
}

// ValuesFor returns the session tree's harvested values.
func (r *Runtime) ValuesFor(rootSessionID string) []Value {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	sess := r.bySession[rootSessionID]
	out := make([]Value, 0, len(sess))
	for _, v := range sess {
		out = append(out, v)
	}
	return out
}

// ValuesForProject returns the evidence of every session tree in one project,
// for screens attributed to a project without a chat.
func (r *Runtime) ValuesForProject(projectID string) []Value {
	if r == nil || strings.TrimSpace(projectID) == "" {
		return nil
	}
	r.mu.RLock()
	resolve := r.projectOf
	var unbound []string
	for root := range r.bySession {
		if _, ok := r.projects[root]; !ok {
			unbound = append(unbound, root)
		}
	}
	r.mu.RUnlock()
	// Resolution may read the store, so it runs outside the lock.
	resolved := make(map[string]string, len(unbound))
	if resolve != nil {
		for _, root := range unbound {
			if project := resolve(root); project != "" {
				resolved[root] = project
			}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for root, project := range resolved {
		r.projects[root] = project
	}
	var out []Value
	for root, sess := range r.bySession {
		if r.projects[root] != projectID {
			continue
		}
		for _, v := range sess {
			out = append(out, v)
		}
	}
	return out
}

// AllValues supports screens without session attribution.
func (r *Runtime) AllValues() []Value {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Value
	for _, sess := range r.bySession {
		for _, v := range sess {
			out = append(out, v)
		}
	}
	return out
}

// Forget drops a session tree's harvested values.
func (r *Runtime) Forget(rootSessionID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.bySession, rootSessionID)
	delete(r.projects, rootSessionID)
}

// Pair is one parsed name/value binding.
type Pair struct {
	Name  string
	Value string
}

// ParseContainer extracts bounded JSON, YAML, and key-value bindings.
func ParseContainer(container string, content []byte) []Pair {
	text := string(content)
	var pairs []Pair
	switch {
	case json.Valid(content):
		var doc any
		if err := json.Unmarshal(content, &doc); err == nil {
			pairs = walkDoc("", doc)
			break
		}
		pairs = parseLines(text)
	case looksYAML(container):
		var doc any
		if err := yaml.Unmarshal(content, &doc); err == nil {
			pairs = walkDoc("", doc)
			break
		}
		pairs = parseLines(text)
	default:
		pairs = parseLines(text)
	}
	out := pairs[:0]
	for _, p := range pairs {
		if harvestable(p.Value) {
			out = append(out, p)
		}
		if len(out) >= MaxValuesPerContainer {
			break
		}
	}
	return out
}

func looksYAML(container string) bool {
	lower := strings.ToLower(container)
	return strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml")
}

// parseLines reads KEY=VALUE bindings: dotenv, ini, and properties shapes.
func parseLines(text string) []Pair {
	var out []Pair
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "[") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexAny(line, "=:")
		if eq <= 0 {
			continue
		}
		name := strings.TrimSpace(line[:eq])
		value := strings.TrimSpace(line[eq+1:])
		value = strings.Trim(value, `"'`)
		if name == "" || value == "" {
			continue
		}
		out = append(out, Pair{Name: name, Value: value})
	}
	return out
}

func walkDoc(prefix string, doc any) []Pair {
	var out []Pair
	switch node := doc.(type) {
	case map[string]any:
		for key, child := range node {
			out = append(out, walkDoc(joinPath(prefix, key), child)...)
		}
	case map[any]any:
		for key, child := range node {
			out = append(out, walkDoc(joinPath(prefix, toKey(key)), child)...)
		}
	case []any:
		for i, child := range node {
			out = append(out, walkDoc(joinPath(prefix, strconv.Itoa(i)), child)...)
		}
	case string:
		if prefix != "" {
			out = append(out, Pair{Name: prefix, Value: node})
		}
	}
	return out
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func toKey(key any) string {
	if s, ok := key.(string); ok {
		return s
	}
	return ""
}

// Container bindings use the stricter harvested floor.
func harvestable(value string) bool {
	if utf8.RuneCountInString(value) < secretmatch.MinNeedleRunes(false) {
		return false
	}
	lower := strings.ToLower(value)
	if lower == "true" || lower == "false" {
		return false
	}
	if _, err := strconv.ParseFloat(value, 64); err == nil {
		return false
	}
	return true
}
