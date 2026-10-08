package sizebudget

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"
)

// ChangeSetEnv names the JSON file describing the change a run judges.
// scripts/budgets.py writes it from the merge base with main.
const ChangeSetEnv = "PW_CHANGE_SET"

// ChangeSet is what a change touched: the lines it added or modified per
// path, and the paths it added or removed. Base is the commit it builds on.
type ChangeSet struct {
	Base    string           `json:"base"`
	Lines   map[string][]int `json:"lines"`
	Added   []string         `json:"added"`
	Removed []string         `json:"removed"`
	// Inspect names paths whose standing the run reports as if touched,
	// so an author can look before editing.
	Inspect []string `json:"inspect"`
}

// LoadChangeSet reads the change set, or explains how to provide one.
func LoadChangeSet() (*ChangeSet, error) {
	file := os.Getenv(ChangeSetEnv)
	if file == "" {
		return nil, fmt.Errorf("%s is not set; run `./task budgets`, which judges the change against the merge base with main", ChangeSetEnv)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read change set: %w", err)
	}
	var change ChangeSet
	if err := json.Unmarshal(raw, &change); err != nil {
		return nil, fmt.Errorf("decode change set: %w", err)
	}
	return &change, nil
}

// TouchesFile reports whether the change modified the file or asked about it.
func (c *ChangeSet) TouchesFile(file string) bool {
	if _, ok := c.Lines[file]; ok {
		return true
	}
	return c.inspects(file)
}

// TouchesLines reports whether the change modified any line of file in
// [first, last], or asked about the file.
func (c *ChangeSet) TouchesLines(file string, first, last int) bool {
	for _, line := range c.Lines[file] {
		if line >= first && line <= last {
			return true
		}
	}
	return c.inspects(file)
}

// TouchesDirectory reports whether the change added or removed a file
// directly inside dir, or asked about the directory.
func (c *ChangeSet) TouchesDirectory(dir string) bool {
	for _, list := range [][]string{c.Added, c.Removed} {
		for _, file := range list {
			if path.Dir(file) == dir {
				return true
			}
		}
	}
	return c.inspects(dir)
}

// TouchesAny reports whether the change modified any listed source, where a
// source may be a file or a directory of files.
func (c *ChangeSet) TouchesAny(sources []string) bool {
	for _, source := range sources {
		prefix := strings.TrimSuffix(source, "/") + "/"
		for file := range c.Lines {
			if file == source || strings.HasPrefix(file, prefix) {
				return true
			}
		}
		if c.inspects(source) {
			return true
		}
	}
	return false
}

func (c *ChangeSet) inspects(target string) bool {
	for _, asked := range c.Inspect {
		asked = strings.TrimSuffix(asked, "/")
		if target == asked || strings.HasPrefix(target, asked+"/") {
			return true
		}
	}
	return false
}
