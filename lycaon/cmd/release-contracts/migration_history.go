package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"
)

func isStepType(expr ast.Expr) bool {
	switch kind := expr.(type) {
	case *ast.Ident:
		return kind.Name == "Step"
	case *ast.SelectorExpr:
		return kind.Sel.Name == "Step"
	default:
		return false
	}
}

func recordStepLiteral(file string, literal *ast.CompositeLit, result map[string]string) error {
	fields := map[string]string{}
	for _, element := range literal.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		name, ok := pair.Key.(*ast.Ident)
		if !ok || (name.Name != "ID" && name.Name != "Checksum") {
			continue
		}
		value, ok := pair.Value.(*ast.BasicLit)
		if !ok || value.Kind != token.STRING {
			return fmt.Errorf("migration %s must use literal identities and checksums", file)
		}
		decoded, err := strconv.Unquote(value.Value)
		if err != nil {
			return err
		}
		fields[name.Name] = decoded
	}
	if len(fields) != 2 || fields["ID"] == "" || len(fields["Checksum"]) != 64 {
		return fmt.Errorf("migration %s has an incomplete identity", file)
	}
	if _, exists := result[fields["ID"]]; exists {
		return fmt.Errorf("duplicate migration identity %s", fields["ID"])
	}
	result[fields["ID"]] = fields["Checksum"]
	return nil
}

func migrationChecksums(source releaseSource) (map[string]string, error) {
	paths, err := source.paths("lycaon/internal/db/migrations")
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, file := range paths {
		if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			continue
		}
		raw, err := source.show(file)
		if err != nil {
			return nil, err
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, raw, 0)
		if err != nil {
			return nil, err
		}
		var parseErr error
		ast.Inspect(parsed, func(node ast.Node) bool {
			literal, ok := node.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if isStepType(literal.Type) {
				parseErr = recordStepLiteral(file, literal, result)
				return false
			}
			arrayType, ok := literal.Type.(*ast.ArrayType)
			if !ok || !isStepType(arrayType.Elt) {
				return true
			}
			for _, elt := range literal.Elts {
				item, ok := elt.(*ast.CompositeLit)
				if !ok || (item.Type != nil && !isStepType(item.Type)) {
					continue
				}
				if parseErr = recordStepLiteral(file, item, result); parseErr != nil {
					return false
				}
			}
			return false
		})
		if parseErr != nil {
			return nil, parseErr
		}
	}
	dbPaths, err := source.paths("lycaon/internal/db")
	if err != nil {
		return nil, err
	}
	for _, rel := range dbPaths {
		if rel != "lycaon/internal/db/released-baselines.json" {
			continue
		}
		raw, err := source.show(rel)
		if err != nil {
			return nil, err
		}
		var baselines []struct {
			Revision int `json:"revision"`
		}
		if err := json.Unmarshal(raw, &baselines); err != nil {
			return nil, err
		}
		revisions := map[int]struct{}{}
		for _, b := range baselines {
			revisions[b.Revision] = struct{}{}
		}
		if len(result) < len(revisions) {
			return nil, fmt.Errorf("migration history has %d steps for %d released schema revisions", len(result), len(revisions))
		}
	}
	return result, nil
}

func checkMigrationHistory(release, current releaseSource) error {
	before, err := migrationChecksums(release)
	if err != nil {
		return err
	}
	after, err := migrationChecksums(current)
	if err != nil {
		return err
	}
	for id, checksum := range before {
		if after[id] != checksum {
			return fmt.Errorf("released migration %s was removed or changed checksum", id)
		}
	}
	return nil
}

func checkPublishedMigrationHistory(ctx context.Context, repoRoot, raw string) error {
	var versions []string
	if err := json.Unmarshal([]byte(raw), &versions); err != nil {
		return err
	}
	if versions == nil {
		return fmt.Errorf("published versions must be an array")
	}
	for _, value := range versions {
		version, err := semver.StrictNewVersion(value)
		if err != nil {
			return err
		}
		if version.LessThan(semver.MustParse("1.0.0")) || version.String() != value {
			return fmt.Errorf("invalid published migration source %s", value)
		}
		source := gitRelease{ctx: ctx, repoRoot: repoRoot, tag: "refs/tags/v" + value}
		if err := checkMigrationHistory(source, diskRelease{root: repoRoot}); err != nil {
			return fmt.Errorf("v%s: %w", value, err)
		}
	}
	return nil
}
