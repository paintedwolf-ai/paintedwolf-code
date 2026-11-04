package main

import (
	"encoding/json"
	"fmt"
	"reflect"

	"gopkg.in/yaml.v3"
)

const capabilityPath = packsRoot + "/painted-wolf/platform/host/anchors/oar-profile.yaml"

type factSignature struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type"`
	Observation string `yaml:"observation"`
}

type codeSignature struct {
	Category string `json:"category"`
	Emit     string `json:"emit"`
	Severity string `json:"severity"`
}

func checkAgentVocabulary(release releaseSource, current releaseSource) error {
	raw, err := current.show(retirementCatalogPath)
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
		if rel != retirementCatalogPath {
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
		if err := checkReleasedRetirements(previous, retired); err != nil {
			return err
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
	if err := checkVocabulary("host fact", before, after, retired.Vocabularies["host_facts"]); err != nil {
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
	if err := checkVocabulary("rejection code", oldCodes, newCodes, retired.Vocabularies["rejection_codes"]); err != nil {
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
	if err := checkContractIdentities(retired.ContentContracts, beforePublic["tools"], afterPublic["tools"], retired.Vocabularies["tools"]); err != nil {
		return err
	}
	for _, kind := range []string{"tools", "workflow_terms", "anchors", "themes"} {
		if err := checkVocabulary(kind, beforePublic[kind], afterPublic[kind], retired.Vocabularies[kind]); err != nil {
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

func codeVocabulary(source releaseSource) (map[string]codeSignature, error) {
	raw, err := source.show("schemas/guidance_registry.json")
	if err != nil {
		return nil, err
	}
	var document struct {
		Codes map[string]codeSignature `json:"hint_codes"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	if document.Codes == nil {
		return map[string]codeSignature{}, nil
	}
	return document.Codes, nil
}

func checkVocabulary[T any](kind string, before, after map[string]T, retired map[string]retirementSuccessors) error {
	for name, successors := range retired {
		if len(successors.Names) == 0 && successors.Contract == nil {
			return fmt.Errorf("retired %s %s has no named successor", kind, name)
		}
		if _, active := after[name]; active {
			return fmt.Errorf("retired %s %s was reused", kind, name)
		}
		for _, successor := range successors.Names {
			if _, ok := after[successor]; !ok {
				return fmt.Errorf("retired %s %s names unavailable successor %s", kind, name, successor)
			}
		}
	}
	for name, prior := range before {
		current, exists := after[name]
		if exists {
			if !reflect.DeepEqual(prior, current) {
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
