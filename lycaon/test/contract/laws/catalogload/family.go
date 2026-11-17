// Package catalogload holds the cross-reference laws of the stock catalog:
// packs, agents, tools, workflows, guidance and notices, read through their
// production loaders.
package catalogload

import (
	"errors"
	"slices"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// Family is the catalog-load rule table.
var Family = laws.Family{Name: "catalog-load", Rules: slices.Concat(
	guidanceCodes,
	toolCatalog,
	configYAML,
	bindingClosure,
	policyCopy,
	topology,
	workflows,
	phaseGraph,
	rulesConfig,
	noticeClosure,
	surfaces,
	providers,
	scanning,
	detectionPacks,
	sessionCatalog,
	toolSchemaArgs,
	rejectTemplates,
)}

var errNotRegistered = errors.New("not registered")
