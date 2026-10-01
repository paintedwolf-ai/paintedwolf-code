package ignores

import (
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/projectignore"
	"gopkg.in/yaml.v3"
)

func AddIgnoreEntry(rootPath string, entry IgnoreEntry) (IgnoreEntry, error) {
	entry.ID = strings.TrimSpace(entry.ID)
	if entry.ID == "" {
		entry.ID = uuid.NewString()
	}
	entry.Reason = strings.TrimSpace(entry.Reason)
	if err := entry.Validate(); err != nil {
		return IgnoreEntry{}, err
	}
	err := projectignore.Edit(rootPath, "findings", func(list *yaml.Node) error {
		var node yaml.Node
		if err := node.Encode(entry); err != nil {
			return err
		}
		list.Content = append(list.Content, &node)
		return nil
	})
	if err != nil {
		return IgnoreEntry{}, err
	}
	return entry, nil
}

func RemoveIgnoreEntry(rootPath, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrIgnoreEntryNotFound
	}
	return projectignore.Edit(rootPath, "findings", func(list *yaml.Node) error {
		kept := make([]*yaml.Node, 0, len(list.Content))
		for _, item := range list.Content {
			if nodeMapValue(item, "id") != id {
				kept = append(kept, item)
			}
		}
		if len(kept) == len(list.Content) {
			return ErrIgnoreEntryNotFound
		}
		list.Content = kept
		return nil
	})
}

func nodeMapValue(node *yaml.Node, key string) string {
	if node == nil || node.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return strings.TrimSpace(node.Content[i+1].Value)
		}
	}
	return ""
}
