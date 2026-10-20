package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLanguageCatalogClosesOverFormsAndStockTools(t *testing.T) {
	root := filepath.Join("..", "..")
	catalog, err := loadLanguageCatalog(root)
	testutil.FailErr(t, "load catalog", err)
	if len(catalog.Tools) != 119 || len(catalog.Commands) != 117 {
		t.Fatalf("catalog scope: %d tools, %d commands", len(catalog.Tools), len(catalog.Commands))
	}
	if len(catalog.Fixtures) < 700 {
		t.Fatalf("option/limit fixture coverage=%d", len(catalog.Fixtures))
	}
	for _, name := range []string{"sed", "stat", "base64", "rg", "git switch", "curl", "jq-edit", "source"} {
		help, ok := catalog.CommandHelp(name)
		if !ok || !strings.Contains(help, name) {
			t.Errorf("missing focused help for %s", name)
		}
	}
}

func TestLanguageCatalogRejectsIncompleteContracts(t *testing.T) {
	root := filepath.Join("..", "..")
	original, err := loadLanguageCatalog(root)
	testutil.FailErr(t, "load original", err)
	for _, test := range []struct {
		name   string
		mutate func(*toolcontract.LanguageCatalog)
	}{
		{"version", func(c *toolcontract.LanguageCatalog) { c.Language.Version = 2 }},
		{"missing command", func(c *toolcontract.LanguageCatalog) { c.Commands = c.Commands[1:] }},
		{"duplicate command", func(c *toolcontract.LanguageCatalog) { c.Commands = append(c.Commands, c.Commands[0]) }},
		{"unknown owner", func(c *toolcontract.LanguageCatalog) { c.Tools[0].Owner = "catalog_is_not_an_owner" }},
		{"missing disposition", func(c *toolcontract.LanguageCatalog) { c.Tools = c.Tools[1:] }},
		{"missing semantic contract", func(c *toolcontract.LanguageCatalog) { delete(c.Contracts, "regex") }},
		{"missing effect mode", func(c *toolcontract.LanguageCatalog) { c.Commands[0].Effects[0].Mode = "" }},
		{"unknown limit", func(c *toolcontract.LanguageCatalog) { c.Commands[0].Limits = append(c.Commands[0].Limits, "absent") }},
		{"duplicate option", func(c *toolcontract.LanguageCatalog) {
			for i := range c.Commands {
				if len(c.Commands[i].Forms) > 0 {
					c.Commands[i].Forms = append(c.Commands[i].Forms, c.Commands[i].Forms[0])
					break
				}
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, err := json.Marshal(original)
			testutil.FailErr(t, "copy facts", err)
			c, err := toolcontract.DecodeLanguageCatalog(data)
			testutil.FailErr(t, "decode facts", err)
			test.mutate(&c)
			if err := validateLanguageCatalog(root, c); err == nil {
				t.Fatal("invalid catalog admitted")
			}
		})
	}
}

func TestLanguageFixtureReferencesCannotSilentlyDisappear(t *testing.T) {
	c, err := loadLanguageCatalog(filepath.Join("..", ".."))
	testutil.FailErr(t, "load", err)
	c.Commands[0].FixtureIDs = append(c.Commands[0].FixtureIDs, "missing")
	if err := validateLanguageFixtures(c); err == nil {
		t.Fatal("missing referenced fixture admitted")
	}
	c.Commands[0].FixtureIDs = c.Commands[0].FixtureIDs[:len(c.Commands[0].FixtureIDs)-1]
	c.Fixtures = append(c.Fixtures, c.Fixtures[0])
	if err := validateLanguageFixtures(c); err == nil {
		t.Fatal("duplicate fixture admitted")
	}
}

func TestLanguageSourcesAreRequiredAndStrict(t *testing.T) {
	source := filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "command-language-v1")
	for _, test := range []struct {
		name   string
		file   string
		change func([]byte) []byte
	}{
		{"missing search", "search.json", func([]byte) []byte { return nil }},
		{"missing regex", "regex.json", func([]byte) []byte { return nil }},
		{"unknown option field", "search.json", func(b []byte) []byte { return []byte(strings.Replace(string(b), `"arity":`, `"arity_typo":`, 1)) }},
		{"unknown effect field", "search.json", func(b []byte) []byte { return []byte(strings.Replace(string(b), `"mode":`, `"when":`, 1)) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			dest := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "tools", "command-language-v1")
			testutil.FailErr(t, "make source fixture", os.MkdirAll(dest, 0o750))
			entries, err := os.ReadDir(source)
			testutil.FailErr(t, "list source", err)
			for _, entry := range entries {
				data, err := os.ReadFile(filepath.Join(source, entry.Name()))
				testutil.FailErr(t, "read fragment", err)
				if entry.Name() == test.file {
					data = test.change(data)
				}
				if data != nil {
					testutil.FailErr(t, "copy fragment", os.WriteFile(filepath.Join(dest, entry.Name()), data, 0o600))
				}
			}
			_, err = loadLanguageCatalog(root)
			expected := "unknown field"
			if strings.HasPrefix(test.name, "missing") {
				expected = "source inventory"
			}
			if err == nil || !strings.Contains(err.Error(), expected) {
				t.Fatalf("expected %s refusal, got %v", expected, err)
			}
		})
	}
}

func TestGeneratedLanguageContractIsCurrent(t *testing.T) {
	testutil.FailErr(t, "generated contract", generateLanguageCatalog(filepath.Join("..", ".."), true))
}
