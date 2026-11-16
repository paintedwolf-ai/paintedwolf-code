package reach

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/archlint"
	"gopkg.in/yaml.v3"
)

// archlint.Finding kinds for declarations nothing in the product emits.
const (
	KindUnemittedCode  = "unemitted-code"
	KindUnproducedFact = "unproduced-fact"
	KindUnfiredAnchor  = "unfired-anchor"
)

// rejectionCodeFact is the fact a policy reads when it only presents a
// rejection the host code raised.
const rejectionCodeFact = "paintedwolf.rejection_code"

// EmitterSpec names, relative to the repository root, where codes, facts and
// anchors are declared and which files only declare or consume them.
type EmitterSpec struct {
	Product  []string `json:"product"`
	Registry string   `json:"registry"`
	Policies string   `json:"policies"`
	Data     []string `json:"data"`
	Facts    string   `json:"facts"`
	Anchors  string   `json:"anchors"`
	// Plumbing lists module-relative files whose constant tables declare or
	// consume identifiers rather than produce them.
	Plumbing []string `json:"plumbing"`
}

// Emitters is a loaded EmitterSpec.
type Emitters struct {
	Product  []string
	Plumbing []string
	codes    map[string]declaredCode
	facts    map[string]string
	anchors  map[string]string
}

type declaredCode struct {
	pos string
	// exempt codes are shown by surfaces this check does not judge.
	exempt bool
	// data codes are named by rule or profile documents the host evaluates.
	data bool
	// anchor is set when a policy fires the code on its own conditions.
	anchor string
}

// LoadEmitters reads the declarations a spec names under repoRoot.
func LoadEmitters(repoRoot string, spec EmitterSpec) (*Emitters, error) {
	if len(spec.Product) == 0 {
		return nil, fmt.Errorf("emitters: no product roots")
	}
	e := &Emitters{Product: spec.Product, Plumbing: spec.Plumbing, codes: map[string]declaredCode{}, facts: map[string]string{}, anchors: map[string]string{}}
	if err := e.loadRegistry(repoRoot, spec.Registry); err != nil {
		return nil, err
	}
	if err := e.loadPolicies(repoRoot, spec.Policies); err != nil {
		return nil, err
	}
	for _, rel := range spec.Data {
		if err := e.loadData(repoRoot, rel); err != nil {
			return nil, err
		}
	}
	if err := e.loadFacts(repoRoot, spec.Facts); err != nil {
		return nil, err
	}
	if err := e.loadAnchors(repoRoot, spec.Anchors); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Emitters) loadRegistry(root, rel string) error {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	var registry struct {
		Codes map[string]struct {
			Emit     string `json:"emit"`
			Severity string `json:"severity"`
		} `json:"hint_codes"`
	}
	if err := json.Unmarshal(raw, &registry); err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	if len(registry.Codes) == 0 {
		return fmt.Errorf("%s declares no codes", rel)
	}
	for code, entry := range registry.Codes {
		exempt := entry.Emit == "banner" || entry.Severity == "warning" ||
			entry.Severity == "info" && !strings.HasPrefix(entry.Emit, "guard:") && !strings.HasPrefix(entry.Emit, "rule:")
		e.codes[code] = declaredCode{pos: rel, exempt: exempt}
	}
	return nil
}

// loadPolicies records the anchor of each policy that fires without echoing a
// host rejection.
func (e *Emitters) loadPolicies(root, rel string) error {
	dir := filepath.Join(root, filepath.FromSlash(rel))
	found := 0
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(filepath.Dir(path)) != "policy" || filepath.Ext(path) != ".yaml" {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var policy struct {
			ID       string   `yaml:"id"`
			On       string   `yaml:"on"`
			Requires []string `yaml:"requires_facts"`
		}
		if err := yaml.Unmarshal(raw, &policy); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		found++
		code, ok := e.codes[policy.ID]
		if !ok || policy.On == "" || containsString(policy.Requires, rejectionCodeFact) {
			return nil
		}
		code.anchor = policy.On
		if pos, err := filepath.Rel(root, path); err == nil {
			code.pos = filepath.ToSlash(pos)
		}
		e.codes[policy.ID] = code
		return nil
	})
	if err == nil && found == 0 {
		err = fmt.Errorf("%s holds no policies", rel)
	}
	return err
}

// loadData marks every registered code a rule or profile document names.
func (e *Emitters) loadData(root, rel string) error {
	path := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	files := []string{path}
	if info.IsDir() {
		files, err = filepath.Glob(filepath.Join(path, "*.yaml"))
		if err != nil {
			return err
		}
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		e.markDataScalars(&doc)
	}
	return nil
}

func (e *Emitters) markDataScalars(node *yaml.Node) {
	if node.Kind == yaml.ScalarNode {
		if code, ok := e.codes[node.Value]; ok {
			code.data = true
			e.codes[node.Value] = code
		}
	}
	for _, child := range node.Content {
		e.markDataScalars(child)
	}
}

func (e *Emitters) loadFacts(root, rel string) error {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	var profile struct {
		Facts []struct {
			Name string `yaml:"name"`
		} `yaml:"host_facts"`
	}
	if err := yaml.Unmarshal(raw, &profile); err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	for _, fact := range profile.Facts {
		e.facts[fact.Name] = rel
	}
	if len(e.facts) == 0 {
		return fmt.Errorf("%s declares no host facts", rel)
	}
	return nil
}

func (e *Emitters) loadAnchors(root, rel string) error {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	var catalog struct {
		Anchors []struct {
			ID string `yaml:"id"`
		} `yaml:"anchors"`
	}
	if err := yaml.Unmarshal(raw, &catalog); err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	for _, anchor := range catalog.Anchors {
		e.anchors[anchor.ID] = rel
	}
	if len(e.anchors) == 0 {
		return fmt.Errorf("%s declares no anchors", rel)
	}
	return nil
}

// findings judges every declaration against the union of the product passes.
func (e *Emitters) findings(passes []*pass) []archlint.Finding {
	flows := map[string]bool{}
	produced := map[string]bool{}
	for _, p := range passes {
		for s := range p.product.flows {
			flows[s] = true
		}
		for key, fields := range p.product.keyFields {
			for field := range fields {
				if p.product.fieldWrites[field] {
					produced[key] = true
				}
			}
		}
	}
	var out []archlint.Finding
	for _, anchor := range sortedKeys(e.anchors) {
		if !flows[anchor] {
			out = append(out, archlint.Finding{Kind: KindUnfiredAnchor, Key: anchor, Pos: e.anchors[anchor],
				Message: "anchor " + anchor + " is declared but the product never fires it"})
		}
	}
	for _, name := range sortedKeys(e.facts) {
		bare := strings.TrimPrefix(name, "paintedwolf.")
		if !flows[name] && !flows[bare] && !produced[name] && !produced[bare] {
			out = append(out, archlint.Finding{Kind: KindUnproducedFact, Key: name, Pos: e.facts[name],
				Message: "host fact " + name + " is declared but the product never produces it"})
		}
	}
	for _, code := range sortedKeys(e.codes) {
		decl := e.codes[code]
		if decl.exempt || decl.data || flows[code] || decl.anchor != "" && flows[decl.anchor] {
			continue
		}
		out = append(out, archlint.Finding{Kind: KindUnemittedCode, Key: code, Pos: decl.pos,
			Message: "code " + code + " is registered but the product never emits it"})
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
