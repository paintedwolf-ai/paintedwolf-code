package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/lycaon/lycaon/internal/fseffect"
	"gopkg.in/yaml.v3"
)

type suite struct {
	Language      string       `yaml:"language"`
	Comment       []string     `yaml:"comment"`
	CommentSuffix string       `yaml:"comment_suffix"`
	Limits        string       `yaml:"limits"`
	Cases         []sourceCase `yaml:"cases"`
}

type sourceCase struct {
	Evidence    []expectedEvidence   `yaml:"evidence"`
	Diagnostics []expectedDiagnostic `yaml:"diagnostics"`
	Coverage    []string             `yaml:"coverage"`
	Name        string               `yaml:"name"`
	File        string               `yaml:"file"`
	Source      string               `yaml:"source"`
	Want        []string             `yaml:"want"`
	Covers      []string             `yaml:"covers"`
}

func loadSuites(root string) ([]suite, error) {
	paths, err := filepath.Glob(filepath.Join(root, "*.yaml"))
	if err != nil {
		return nil, err
	}
	var out []suite
	for _, path := range paths {
		raw, err := os.ReadFile(path) // #nosec G304 -- path comes from the fixed repository corpus glob.
		if err != nil {
			return nil, err
		}
		var item suite
		if err := decodeCorpus(raw, &item); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if len(item.Cases) == 0 {
			return nil, fmt.Errorf("%s has no cases", path)
		}
		for _, tc := range item.Cases {
			if err := validateCaseEvidence(tc); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// validateCoverage checks every corpus reference against the compiled selection.
// complete additionally requires the corpus to be the whole shipping corpus: one
// suite per supported language, and vulnerable plus safe cases for every rule.
// An assignment corpus covers one framework and is checked for references only.
func validateCoverage(bundle []byte, suites []suite, complete bool) error {
	var selected struct {
		Rules []struct {
			ID       string `yaml:"id"`
			Metadata struct {
				Purpose string `yaml:"purpose"`
			} `yaml:"metadata"`
		} `yaml:"rules"`
	}
	if err := yaml.Unmarshal(bundle, &selected); err != nil {
		return err
	}
	active := make(map[string]bool)
	for _, rule := range selected.Rules {
		active[rule.ID] = rule.Metadata.Purpose != "coverage"
	}
	coverage := make(map[string]bool)
	for _, rule := range selected.Rules {
		if rule.Metadata.Purpose == "coverage" {
			coverage[rule.ID] = false
		}
	}
	positive, negative, languages := make(map[string]bool), make(map[string]bool), make(map[string]bool)
	for _, item := range suites {
		if languages[item.Language] {
			return fmt.Errorf("duplicate corpus language %s", item.Language)
		}
		languages[item.Language] = true
		for _, tc := range item.Cases {
			for _, id := range tc.Coverage {
				if _, ok := coverage[id]; !ok {
					return fmt.Errorf("unknown coverage observation %s", id)
				}
				coverage[id] = true
			}
			for _, id := range append(slices.Clone(tc.Want), tc.Covers...) {
				if !active[id] {
					return fmt.Errorf("%s/%s references inactive rule %s", item.Language, tc.Name, id)
				}
			}
			for _, id := range tc.Want {
				positive[id] = true
			}
			for _, id := range tc.Covers {
				if !slices.Contains(tc.Want, id) {
					negative[id] = true
				}
			}
		}
	}
	if !complete {
		return nil
	}
	for _, lang := range filekind.SupportedLanguages() {
		if !languages[lang] {
			return fmt.Errorf("missing security corpus for %s", lang)
		}
	}
	for _, rule := range selected.Rules {
		if rule.Metadata.Purpose != "coverage" && (!positive[rule.ID] || !negative[rule.ID]) {
			return fmt.Errorf("rule %s requires vulnerable and safe regression cases", rule.ID)
		}
	}
	for id, covered := range coverage {
		if !covered {
			return fmt.Errorf("coverage observation %s has no case", id)
		}
	}
	return nil
}

func materialize(root string, suites []suite, language string) (map[string]sourceCase, error) {
	out := make(map[string]sourceCase)
	for _, item := range suites {
		if language != "" && item.Language != language {
			continue
		}
		for _, tc := range item.Cases {
			if err := writeCase(root, item.Language, tc, out); err != nil {
				return nil, err
			}
			if len(tc.Want) > 0 && len(item.Comment) > 0 {
				prefixed := tc
				prefixed.Name += "-after-comment"
				prefixed.Evidence = shiftCaseEvidence(tc.Evidence)
				prefixed.Diagnostics = slices.Clone(tc.Diagnostics)
				for index := range prefixed.Diagnostics {
					if prefixed.Diagnostics[index].Line > 0 {
						prefixed.Diagnostics[index].Line++
					}
				}
				if len(item.Comment) == 1 {
					prefixed.Source = item.Comment[0] + " TODO: token private password FIXME\n" + tc.Source
				} else {
					prefixed.Source = item.Comment[0] + " TODO: token private password FIXME " + item.Comment[1] + "\n" + tc.Source
				}
				if item.Language == "php" {
					prefixed.Source = "<?php // TODO: token private password FIXME\n" + strings.TrimPrefix(tc.Source, "<?php ")
				}
				if err := writeCase(root, item.Language, prefixed, out); err != nil {
					return nil, err
				}
				commented := tc
				commented.Name += "-comment"
				commented.Want = nil
				commented.Evidence = nil
				commented.Diagnostics = nil
				if len(item.Comment) == 1 {
					commented.Source = item.Comment[0] + " " + strings.ReplaceAll(strings.TrimSuffix(tc.Source, "\n"), "\n", "\n"+item.Comment[0]+" ") + "\n"
				} else {
					commented.Source = item.Comment[0] + "\n" + tc.Source + "\n" + item.Comment[1] + "\n"
				}
				if item.Language == "php" {
					commented.Source = "<?php\n// " + strings.ReplaceAll(strings.TrimPrefix(tc.Source, "<?php "), "\n", "\n// ")
				}
				commented.Source += item.CommentSuffix
				if err := writeCase(root, item.Language, commented, out); err != nil {
					return nil, err
				}
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no cases for language %q", language)
	}
	return out, nil
}

func writeCase(root, language string, tc sourceCase, out map[string]sourceCase) error {
	if tc.Name == "" || tc.Source == "" || !filepath.IsLocal(tc.File) || !filepath.IsLocal(tc.Name) {
		return fmt.Errorf("%s: invalid source case %q", language, tc.Name)
	}
	path := filepath.Join("source", language, tc.Name, tc.File)
	if _, exists := out[path]; exists {
		return fmt.Errorf("duplicate case %s", path)
	}
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: root, Rel: path},
		Source:   strings.NewReader(tc.Source), Mode: 0o600, DirMode: 0o750,
	}); err != nil {
		return err
	}
	out[path] = tc
	return nil
}
