package releasecontract

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
)

const (
	themeUnitSchemaPath = "docs/schemas/theme-unit.schema.json"
	projectRootsPath    = "lycaon/internal/projectroot/roots.go"
)

// virtualRootLabel matches the constants that name host namespaces agents address like roots.
var virtualRootLabel = regexp.MustCompile(`^Virtual[A-Z][A-Za-z]*Label$`)

type schemaNode struct {
	Type       any                   `json:"type"`
	Properties map[string]schemaNode `json:"properties"`
}

// themeKeys lists every property path a theme unit may set, with its JSON type.
// Theme units refuse unknown keys, so a removed or retyped path breaks themes
// written for the release.
func themeKeys(source releaseSource) (map[string]string, error) {
	raw, err := source.show(themeUnitSchemaPath)
	if err != nil {
		return nil, err
	}
	var root schemaNode
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("%s: %w", themeUnitSchemaPath, err)
	}
	if len(root.Properties) == 0 {
		return nil, fmt.Errorf("%s declares no properties", themeUnitSchemaPath)
	}
	keys := map[string]string{}
	var walk func(prefix string, node schemaNode) error
	walk = func(prefix string, node schemaNode) error {
		for name, child := range node.Properties {
			key := prefix + name
			signature, err := json.Marshal(child.Type)
			if err != nil {
				return err
			}
			keys[key] = string(signature)
			if err := walk(key+"/", child); err != nil {
				return err
			}
		}
		return nil
	}
	return keys, walk("", root)
}

// virtualRoots reads the virtual root labels from the projectroot constants.
func virtualRoots(source releaseSource) (map[string]string, error) {
	raw, err := source.show(projectRootsPath)
	if err != nil {
		return nil, err
	}
	file, err := parser.ParseFile(token.NewFileSet(), projectRootsPath, raw, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	labels := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			for i, name := range value.Names {
				if !virtualRootLabel.MatchString(name.Name) || i >= len(value.Values) {
					continue
				}
				literal, ok := value.Values[i].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return nil, fmt.Errorf("virtual root %s is not a string literal", name.Name)
				}
				label, err := strconv.Unquote(literal.Value)
				if err != nil {
					return nil, err
				}
				labels[label] = ""
			}
		}
	}
	if len(labels) == 0 {
		return nil, fmt.Errorf("%s declares no virtual roots", projectRootsPath)
	}
	return labels, nil
}
