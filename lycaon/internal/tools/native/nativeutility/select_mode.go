package nativeutility

import (
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// SelectMode names the effect mode argv selects through the catalog binder, as
// each family's Prepare binding does: empty or unknown argv, or a form the
// grammar refuses, selects no mode. modeFor derives the family-specific mode
// from the bound form.
func SelectMode(specs map[string]toolcontract.CommandSpecification, argv commandprogram.Fields, modeFor func(toolcontract.CommandSpecification, commandprogram.BoundArgv) string) (string, bool) {
	if len(argv) == 0 {
		return "", false
	}
	spec, ok := specs[argv[0].Value]
	if !ok {
		return "", false
	}
	bound, err := commandprogram.CatalogBinder{}.Bind(spec, argv)
	if err != nil {
		return "", false
	}
	mode := modeFor(spec, bound)
	return mode, mode != ""
}
