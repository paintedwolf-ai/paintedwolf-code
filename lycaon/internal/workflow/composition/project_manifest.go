package composition

import (
	"fmt"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"gopkg.in/yaml.v3"
)

// Existing drafts contain only the resolved manifest.
func projectManifestYAML(authored, trigger string, effective workflowdef.Manifest) (string, error) {
	if authored == "" {
		return workflowdef.MarshalManifestYAML(effective)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(authored), &doc); err != nil {
		return "", err
	}
	mapping := doc.Content[0]
	setProjectField(mapping, "workflow_format", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: "2"})
	if trigger != "" {
		setProjectField(mapping, "trigger", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: trigger})
	}
	data, err := yaml.Marshal(&doc)
	if err != nil {
		return "", err
	}
	if _, err := workflowdef.ParseManifestYAML(data); err != nil {
		if ds := workflowvalidation.PredicateLoadErrors(err, ""); len(ds) > 0 {
			return "", &ComposeValidationFailed{Errors: ds}
		}
		return "", fmt.Errorf("validate project manifest: %w", err)
	}
	return string(data), nil
}

func setProjectField(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}
