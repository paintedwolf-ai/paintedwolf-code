package textutil

import (
	"github.com/lycaon/lycaon/internal/commandprogram"
)

var fileUtilities = map[string]bool{
	"sort": true, "uniq": true, "paste": true, "join": true,
	"comm": true, "tac": true, "sed": true, "awk": true,
}

func PathUtility(name string) bool { return fileUtilities[name] }

// pathTargets projects file operands and parsed script effects from the same
// bound grammar the utility executes.
func (o *Owners) pathTargets(argv commandprogram.BoundArgv, plan *preparedSed) []commandprogram.PathTarget {
	targets := []commandprogram.PathTarget{}
	add := func(path string, write bool) {
		if path != "" && path != "-" {
			targets = append(targets, commandprogram.PathTarget{Path: path, Write: write})
		}
	}
	for _, item := range argv.Items {
		if item.Option == nil || item.Option.ValueRole != "path" {
			continue
		}
		for _, value := range item.Option.Values {
			add(value.Value, argv.Command == "sort" && item.Option.Key == "-o")
		}
	}
	operands := argv.Operands().Values()
	switch argv.Command {
	case "sort", "paste", "join", "comm", "tac":
		for _, operand := range operands {
			add(operand, false)
		}
	case "uniq":
		if len(operands) > 0 {
			add(operands[0], false)
		}
		if len(operands) > 1 {
			add(operands[1], true)
		}
	case "awk":
		if _, ok := argv.Last("-f"); !ok && len(operands) > 0 {
			operands = operands[1:]
		}
		for _, operand := range operands {
			add(operand, false)
		}
	case "sed":
		if plan == nil {
			break
		}
		if plan.program != nil {
			for _, path := range plan.program.ReadFiles() {
				add(path, false)
			}
			for _, path := range plan.program.WriteFiles() {
				add(path, true)
			}
		}
		for _, file := range plan.source.files {
			add(file.Value, plan.source.inPlace)
			if plan.source.inPlace && plan.source.suffix != "" {
				add(backupName(file.Value, plan.source.suffix), true)
			}
		}
	}
	return targets
}
