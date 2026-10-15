package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/lycaon/lycaon/internal/extpacks"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"gopkg.in/yaml.v3"
)

func checkPackAPIs(repoRoot string, out io.Writer) error {
	root := filepath.Join(repoRoot, filepath.FromSlash(packsRoot))
	packs, err := filepath.Glob(filepath.Join(root, "*", "*", "extension.yaml"))
	if err != nil {
		return err
	}
	if len(packs) == 0 {
		return fmt.Errorf("no bundled extension manifests found")
	}
	host, err := sourceExtensionAPI(diskRelease{root: repoRoot})
	if err != nil {
		return err
	}
	for _, file := range packs {
		raw, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		manifest, err := extpacks.ParseManifest(file, raw)
		if err != nil {
			return err
		}
		constraint, err := semver.NewConstraint(manifest.Compatibility.ExtensionAPI)
		if err != nil {
			return err
		}
		if !constraint.Check(host) {
			return fmt.Errorf("%s requires unavailable extension API %s", manifest.ID, manifest.Compatibility.ExtensionAPI)
		}
		if err := checkPackAPI(filepath.Dir(file), manifest); err != nil {
			return fmt.Errorf("%s: %w", manifest.ID, err)
		}
		fmt.Fprintf(out, "Bundled extension API: %s %s\n", manifest.ID, manifest.Compatibility.ExtensionAPI)
	}
	return nil
}

func checkPackAPI(root string, manifest extpacks.Manifest) error {
	constraint, err := semver.NewConstraint(manifest.Compatibility.ExtensionAPI)
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if isIgnoredPackFile(entry.Name()) {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("bundled pack contains symlink %s", rel)
		}
		if entry.IsDir() {
			if rel == "archive" {
				return requireAPIEpoch(constraint, "1.1.0", rel)
			}
			return nil
		}
		if !strings.HasPrefix(extpacks.UnitIDFor(manifest.ID, rel), extpacks.WorkflowUnitIDPrefix) {
			return nil
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		var document yaml.Node
		if err := yaml.Unmarshal(raw, &document); err != nil {
			return err
		}
		for _, field := range workflowdef.ManifestFieldInventory() {
			if field.ExtensionAPI != "" && yamlHasPath(&document, strings.Split(field.Path, ".")) {
				if err := requireAPIEpoch(constraint, field.ExtensionAPI, rel+":"+field.Path); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// API epochs name published interpreter contracts, independent of product patches.
func requireAPIEpoch(constraint *semver.Constraints, required, source string) error {
	floor, err := semver.StrictNewVersion(required)
	if err != nil {
		return err
	}
	epochs := map[string]bool{"1.0.0": true, extpacks.ExtensionAPIVersion: true}
	for _, field := range workflowdef.ManifestFieldInventory() {
		if field.ExtensionAPI != "" {
			epochs[field.ExtensionAPI] = true
		}
	}
	for epoch := range epochs {
		version, err := semver.StrictNewVersion(epoch)
		if err != nil {
			return err
		}
		if version.LessThan(floor) && constraint.Check(version) {
			return fmt.Errorf("%s needs extension API %s; declared range %s admits API %s", source, required, constraint, epoch)
		}
	}
	return nil
}

func yamlHasPath(node *yaml.Node, keys []string) bool {
	if len(keys) == 0 {
		return true
	}
	switch node.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range node.Content {
			if yamlHasPath(child, keys) {
				return true
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == keys[0] && yamlHasPath(node.Content[i+1], keys[1:]) {
				return true
			}
		}
	}
	return false
}

// The selected source supplies the API version, even when the controller advances.
func sourceExtensionAPI(source releaseSource) (*semver.Version, error) {
	const rel = "lycaon/internal/extpacks/manifest.go"
	raw, err := source.show(rel)
	if err != nil {
		return nil, err
	}
	file, err := parser.ParseFile(token.NewFileSet(), rel, raw, 0)
	if err != nil {
		return nil, err
	}
	for _, declaration := range file.Decls {
		group, ok := declaration.(*ast.GenDecl)
		if !ok || group.Tok != token.CONST {
			continue
		}
		for _, spec := range group.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range value.Names {
				if name.Name != "ExtensionAPIVersion" {
					continue
				}
				if i >= len(value.Values) {
					return nil, fmt.Errorf("extension API version has no literal value")
				}
				literal, ok := value.Values[i].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return nil, fmt.Errorf("extension API version must be a string literal")
				}
				version, err := strconv.Unquote(literal.Value)
				if err != nil {
					return nil, err
				}
				return semver.StrictNewVersion(version)
			}
		}
	}
	return nil, fmt.Errorf("candidate source has no extension API version")
}
