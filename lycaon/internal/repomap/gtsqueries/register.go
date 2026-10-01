package gtsqueries

import (
	"strings"

	"github.com/odvcencio/gotreesitter/grammars"
)

func init() {
	registerAll()
}

func registerAll() {
	for name, query := range tagsQueries {
		registerTagsQuery(name, query)
	}
}

func registerTagsQuery(name, tagsQuery string) {
	name = strings.TrimSpace(name)
	tagsQuery = strings.TrimSpace(tagsQuery)
	if name == "" || tagsQuery == "" {
		return
	}
	entry := grammars.DetectLanguageByName(name)
	if entry == nil {
		return
	}
	patched := *entry
	patched.TagsQuery = tagsQuery
	grammars.Register(patched)
}
