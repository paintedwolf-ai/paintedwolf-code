package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/lycaon/lycaon/internal/configlayout"
	"gopkg.in/yaml.v3"
)

const capabilityPath = packsRoot + "/painted-wolf/platform/host/anchors/oar-profile.yaml"

type vocabularyRetirements struct {
	FormatVersion int                            `json:"format_version"`
	Vocabularies  map[string]map[string][]string `json:"vocabularies"`
}

var vocabularyKinds = []string{"host_facts", "rejection_codes", "tools", "workflow_terms", "anchors", "themes"}

type factSignature struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type"`
	Observation string `yaml:"observation"`
}

func checkAgentVocabulary(release releaseSource, current releaseSource) error {
	raw, err := os.ReadFile(filepath.Join(configlayout.FindModuleRoot(), "..", "schemas", "agent-public-retirements.json"))
	if err != nil {
		return err
	}
	retired, err := parseRetirements(raw)
	if err != nil {
		return err
	}
	paths, err := release.paths("schemas")
	if err != nil {
		return err
	}
	for _, rel := range paths {
		if rel != "schemas/agent-public-retirements.json" {
			continue
		}
		raw, err := release.show(rel)
		if err != nil {
			return err
		}
		previous, err := parseRetirements(raw)
		if err != nil {
			return err
		}
		for kind, names := range previous.Vocabularies {
			for name := range names {
				if _, ok := retired.Vocabularies[kind][name]; !ok {
					return fmt.Errorf("released retirement %s %s was removed", kind, name)
				}
			}
		}
	}
	before, err := factVocabulary(release)
	if err != nil {
		return err
	}
	after, err := factVocabulary(current)
	if err != nil {
		return err
	}
	if err := checkVocabulary("host fact", before, after, retired.Vocabularies["host_facts"], true); err != nil {
		return err
	}
	oldCodes, err := codeVocabulary(release)
	if err != nil {
		return err
	}
	newCodes, err := codeVocabulary(current)
	if err != nil {
		return err
	}
	if err := checkVocabulary("rejection code", oldCodes, newCodes, retired.Vocabularies["rejection_codes"], false); err != nil {
		return err
	}
	beforePublic, err := publicVocabularies(release)
	if err != nil {
		return err
	}
	afterPublic, err := publicVocabularies(current)
	if err != nil {
		return err
	}
	for _, kind := range []string{"tools", "workflow_terms", "anchors", "themes"} {
		if err := checkVocabulary(kind, beforePublic[kind], afterPublic[kind], retired.Vocabularies[kind], true); err != nil {
			return err
		}
	}
	return nil
}

func factVocabulary(source releaseSource) (map[string]factSignature, error) {
	raw, err := source.show(capabilityPath)
	if err != nil {
		return nil, err
	}
	var document struct {
		Facts []factSignature `yaml:"host_facts"`
	}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	result := map[string]factSignature{}
	for _, fact := range document.Facts {
		if _, ok := result[fact.Name]; ok {
			return nil, fmt.Errorf("duplicate host fact %s", fact.Name)
		}
		result[fact.Name] = fact
	}
	return result, nil
}

func codeVocabulary(source releaseSource) (map[string]struct{}, error) {
	raw, err := source.show("schemas/guidance_registry.json")
	if err != nil {
		return nil, err
	}
	var document struct {
		Codes map[string]json.RawMessage `json:"hint_codes"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	result := map[string]struct{}{}
	for code := range document.Codes {
		result[code] = struct{}{}
	}
	return result, nil
}

func checkVocabulary[T any](kind string, before, after map[string]T, retired map[string][]string, signatures bool) error {
	for name, successors := range retired {
		if len(successors) == 0 {
			return fmt.Errorf("retired %s %s has no named successor", kind, name)
		}
		if _, active := after[name]; active {
			return fmt.Errorf("retired %s %s was reused", kind, name)
		}
		for _, successor := range successors {
			if _, ok := after[successor]; !ok {
				return fmt.Errorf("retired %s %s names unavailable successor %s", kind, name, successor)
			}
		}
	}
	for name, prior := range before {
		current, exists := after[name]
		if exists {
			if signatures && !reflect.DeepEqual(prior, current) {
				return fmt.Errorf("%s %s changed its signature", kind, name)
			}
			continue
		}
		if _, ok := retired[name]; !ok {
			return fmt.Errorf("%s %s was removed without a named successor", kind, name)
		}
	}
	return nil
}

func parseRetirements(raw []byte) (vocabularyRetirements, error) {
	var retired vocabularyRetirements
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&retired); err != nil {
		return retired, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return retired, fmt.Errorf("retirement catalog must contain one JSON document")
	}
	if retired.FormatVersion != 1 || len(retired.Vocabularies) != len(vocabularyKinds) {
		return retired, fmt.Errorf("unsupported or incomplete agent-public retirement catalog")
	}
	for _, kind := range vocabularyKinds {
		if retired.Vocabularies[kind] == nil {
			return retired, fmt.Errorf("missing retirement vocabulary %s", kind)
		}
	}
	return retired, nil
}
