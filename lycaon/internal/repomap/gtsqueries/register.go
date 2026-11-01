package gtsqueries

import (
	"os"
	"strings"

	"github.com/odvcencio/gotreesitter/grammars"
)

func init() {
	if !isHelperProcess(os.Args) {
		registerAll()
	}
}

func isHelperProcess(args []string) bool {
	return len(args) > 1 && (args[1] == "__command-supervisor" || args[1] == "__confine-exec" || args[1] == "__broker-relay" || args[1] == "--create-performer")
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
