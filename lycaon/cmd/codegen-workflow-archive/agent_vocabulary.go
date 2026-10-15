package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/lycaon/lycaon/internal/configlayout"
	"gopkg.in/yaml.v3"
)

const capabilityPath = packsRoot + "/painted-wolf/platform/host/anchors/oar-profile.yaml"

type vocabularyRetirements struct {
	FormatVersion int                 `json:"format_version"`
	Facts         map[string][]string `json:"facts"`
	Codes         map[string][]string `json:"rejection_codes"`
}

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
	var retired vocabularyRetirements
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&retired); err != nil {
		return err
	}
	if retired.FormatVersion != 1 {
		return fmt.Errorf("unsupported agent-public retirement format %d", retired.FormatVersion)
	}
	before, err := factVocabulary(release)
	if err != nil {
		return err
	}
	after, err := factVocabulary(current)
	if err != nil {
		return err
	}
	if err := checkVocabulary("host fact", before, after, retired.Facts, true); err != nil {
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
	return checkVocabulary("rejection code", oldCodes, newCodes, retired.Codes, false)
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
	}
	for name, prior := range before {
		current, exists := after[name]
		if exists {
			if signatures && !reflect.DeepEqual(prior, current) {
				return fmt.Errorf("%s %s changed its signature", kind, name)
			}
			continue
		}
		successors, ok := retired[name]
		if !ok {
			return fmt.Errorf("%s %s was removed without a named successor", kind, name)
		}
		for _, successor := range successors {
			if _, ok := after[successor]; !ok {
				return fmt.Errorf("retired %s %s names unavailable successor %s", kind, name, successor)
			}
		}
	}
	return nil
}
