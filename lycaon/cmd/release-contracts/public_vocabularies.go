package main

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

func publicVocabularies(source releaseSource) (map[string]map[string]string, error) {
	result := map[string]map[string]string{"tools": {}, "workflow_terms": {}, "anchors": {}, "themes": {}}
	paths, err := source.paths(packsRoot)
	if err != nil {
		return nil, err
	}
	for _, file := range paths {
		if strings.Contains(file, "/tools/schemas/") && strings.HasSuffix(file, ".yaml") {
			result["tools"][strings.TrimSuffix(path.Base(file), ".yaml")] = ""
		}
		if strings.Contains(file, "/contributions/themes/") && strings.HasSuffix(file, ".yaml") {
			raw, err := source.show(file)
			if err != nil {
				return nil, err
			}
			var theme struct {
				ID string `yaml:"id"`
			}
			if err := yaml.Unmarshal(raw, &theme); err != nil {
				return nil, err
			}
			if theme.ID == "" {
				return nil, fmt.Errorf("theme identity missing: %s", file)
			}
			result["themes"][theme.ID] = ""
		}
	}
	raw, err := source.show(packsRoot + "/painted-wolf/platform/tools/native-tools.yaml")
	if err != nil {
		return nil, err
	}
	var tools struct {
		Native   map[string][]string `yaml:"native"`
		Produced []string            `yaml:"host_produced_tools"`
		Prefixes []string            `yaml:"runtime_registered_prefixes"`
	}
	if err := yaml.Unmarshal(raw, &tools); err != nil {
		return nil, err
	}
	for _, names := range tools.Native {
		for _, name := range names {
			result["tools"][name] = ""
		}
	}
	for _, name := range tools.Produced {
		result["tools"][name] = ""
	}
	for _, prefix := range tools.Prefixes {
		result["tools"][prefix+"*"] = ""
	}
	raw, err = source.show("schemas/workflow_vocabulary.json")
	if err != nil {
		return nil, err
	}
	var vocabulary struct {
		Entries []struct {
			ID            string `json:"id"`
			Layer         string `json:"layer"`
			Domain        string `json:"domain"`
			Parameterized bool   `json:"parameterized"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &vocabulary); err != nil {
		return nil, err
	}
	for _, term := range vocabulary.Entries {
		signature, err := json.Marshal([]any{term.Layer, term.Domain, term.Parameterized})
		if err != nil {
			return nil, err
		}
		if _, ok := result["workflow_terms"][term.ID]; ok {
			return nil, fmt.Errorf("duplicate workflow term %s", term.ID)
		}
		result["workflow_terms"][term.ID] = string(signature)
	}
	raw, err = source.show(packsRoot + "/painted-wolf/platform/host/anchors/catalog.yaml")
	if err != nil {
		return nil, err
	}
	var anchors struct {
		Anchors []struct {
			ID string `yaml:"id"`
		} `yaml:"anchors"`
	}
	if err := yaml.Unmarshal(raw, &anchors); err != nil {
		return nil, err
	}
	for _, anchor := range anchors.Anchors {
		if _, ok := result["anchors"][anchor.ID]; ok {
			return nil, fmt.Errorf("duplicate anchor %s", anchor.ID)
		}
		result["anchors"][anchor.ID] = ""
	}
	return result, nil
}
