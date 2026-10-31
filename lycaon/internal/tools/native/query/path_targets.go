package query

import (
	"context"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func queryPathTargets(argv commandprogram.BoundArgv, paths *projectpaths.ProgramPaths) []commandprogram.PathTarget {
	targets := []commandprogram.PathTarget{}
	add := func(path string, write bool) {
		if path != "" && path != "-" {
			targets = append(targets, commandprogram.PathTarget{Path: path, Write: write})
		}
	}
	flag := func(key string) bool { _, ok := argv.Last(key); return ok }
	for _, item := range argv.Items {
		if item.Option == nil {
			continue
		}
		switch item.Option.ValueRole {
		case "path":
			for _, value := range item.Option.Values {
				add(value.Value, argv.Command == "jq-edit" && item.Option.Key == "--path")
			}
		case "name-path-pair":
			if len(item.Option.Values) == 2 {
				add(item.Option.Values[1].Value, false)
			}
		}
	}
	if argv.Command == "jq-edit" {
		return targets
	}
	operands := argv.Operands().Values()
	if argv.Command == "yq" && !pythonYqForm(argv) {
		if len(operands) > 0 && (operands[0] == "eval" || operands[0] == "e" || operands[0] == "eval-all" || operands[0] == "ea") {
			operands = operands[1:]
		}
		// Use the evaluator's file-versus-expression decision before projecting
		// file operands. An expression never becomes a path approval target.
		if _, explicit := argv.Last("--expression"); !explicit && len(operands) > 0 && operands[0] != "-" && !yqImplicitFile(context.Background(), paths, argv, operands[0]) {
			operands = operands[1:]
		}
		for _, path := range operands {
			add(path, flag("-i"))
		}
		return targets
	}
	haveQuery := flag("-f")
	positional := false
	for _, item := range argv.Items {
		if item.Option != nil {
			if item.Option.Key == "--args" || item.Option.Key == "--jsonargs" {
				positional = true
			}
			continue
		}
		if !haveQuery {
			haveQuery = true
			continue
		}
		if !positional {
			add(item.Operand.Value, argv.Command == "yq" && flag("-i"))
		}
	}
	return targets
}

func pythonYqForm(argv commandprogram.BoundArgv) bool {
	if argv.Command != "yq" {
		return false
	}
	for _, key := range pythonYqFlags {
		if _, ok := argv.Last(key); ok {
			return true
		}
	}
	return false
}
