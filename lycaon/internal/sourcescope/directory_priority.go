package sourcescope

import (
	"fmt"
	"path"
	"strings"

	"github.com/lycaon/lycaon/config"
)

type directoryPriorityCatalog struct {
	Version int                      `yaml:"version"`
	Groups  []directoryPriorityGroup `yaml:"groups"`
}

type directoryPriorityGroup struct {
	ID        string   `yaml:"id"`
	Why       string   `yaml:"why"`
	Languages []string `yaml:"languages"`
	// Collapse defaults to true: a deferred directory is closed by a recursive
	// expansion unless its group declares otherwise.
	Tier     string   `yaml:"tier"`
	Collapse *bool    `yaml:"collapse"`
	Patterns []string `yaml:"patterns"`
}

// directoryPriority is the traversal order and, within it, the directories a
// recursive expansion leaves closed.
type directoryPriority struct {
	Deferred   []string
	Collapsed  []string
	Boundaries []string
}

func loadDirectoryPriority() (directoryPriority, error) {
	data, err := config.Read(config.SourceDirectoryPriority)
	if err != nil {
		return directoryPriority{}, fmt.Errorf("read directory priority: %w", err)
	}
	return parseDirectoryPriority(data)
}

func parseDirectoryPriority(data []byte) (directoryPriority, error) {
	var catalog directoryPriorityCatalog
	if err := config.DecodeYAML(data, &catalog); err != nil {
		return directoryPriority{}, fmt.Errorf("parse directory priority: %w", err)
	}
	if catalog.Version != 1 || len(catalog.Groups) == 0 {
		return directoryPriority{}, fmt.Errorf("directory priority requires version 1 and nonempty groups")
	}
	groups, seen := make(map[string]bool), make(map[string]bool)
	var priority directoryPriority
	collapsing, expandable := make(map[string]bool), make(map[string]bool)
	for _, group := range catalog.Groups {
		if strings.TrimSpace(group.ID) == "" || groups[group.ID] || strings.TrimSpace(group.Why) == "" || len(group.Patterns) == 0 {
			return directoryPriority{}, fmt.Errorf("invalid directory priority group %q", group.ID)
		}
		if group.Tier != "" && group.Tier != "source" && group.Tier != "boundary" {
			return directoryPriority{}, fmt.Errorf("invalid directory tier %q", group.Tier)
		}
		groups[group.ID] = true
		for _, pattern := range group.Patterns {
			if err := validateDirectoryPattern(pattern); err != nil {
				return directoryPriority{}, fmt.Errorf("directory priority group %q: %w", group.ID, err)
			}
			if !seen[pattern] {
				seen[pattern] = true
				priority.Deferred = append(priority.Deferred, pattern)
			}
			if group.Collapse != nil && !*group.Collapse {
				expandable[pattern] = true
			} else {
				collapsing[pattern] = true
				if group.Tier == "boundary" {
					priority.Boundaries = append(priority.Boundaries, pattern)
				}
			}
		}
	}
	// A pattern any group declares expandable stays expandable everywhere, so a
	// committed dependency tree is not closed by a name another ecosystem builds into.
	boundarySet := make(map[string]bool)
	for _, pattern := range priority.Boundaries {
		boundarySet[pattern] = true
	}
	priority.Boundaries = nil
	for _, pattern := range priority.Deferred {
		if boundarySet[pattern] && !expandable[pattern] {
			priority.Boundaries = append(priority.Boundaries, pattern)
		}
		if collapsing[pattern] && !expandable[pattern] {
			priority.Collapsed = append(priority.Collapsed, pattern)
		}
	}
	return priority, nil
}

func validateDirectoryPattern(pattern string) error {
	if pattern == "" || strings.TrimSpace(pattern) != pattern || strings.ContainsAny(pattern, "\\\n\r") || strings.ContainsAny(pattern[:1], "/!#") {
		return fmt.Errorf("invalid directory pattern %q", pattern)
	}
	for _, segment := range strings.Split(pattern, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("invalid directory pattern %q", pattern)
		}
	}
	if _, err := path.Match(pattern, ""); err != nil {
		return fmt.Errorf("invalid directory pattern %q: %w", pattern, err)
	}
	return nil
}
