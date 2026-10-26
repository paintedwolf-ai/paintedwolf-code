package textutil

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/tools/native/textutil/sedscript"
)

var fileUtilities = map[string]bool{
	"sort": true, "uniq": true, "paste": true, "join": true,
	"comm": true, "tac": true, "sed": true, "awk": true,
}

func PathUtility(name string) bool { return fileUtilities[name] }

// pathTargets projects file operands and parsed script effects from the same
// bound grammar the utility executes.
func (o *Owners) pathTargets(argv commandprogram.BoundArgv, parsed *sedscript.Program) []commandprogram.PathTarget {
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
		var scripts []string
		for _, item := range argv.Items {
			if item.Option != nil && item.Option.Key == "-e" {
				scripts = append(scripts, item.Option.Values[0].Value)
			}
		}
		bsdSuffix := ""
		if len(scripts) == 0 && !argvFlag(argv, "-f") && len(operands) > 0 {
			// BSD's separate backup suffix is part of the bound sed form.
			fields := argv.Operands()
			if o.bsdSuffix(context.Background(), argv, fields) {
				bsdSuffix = operands[0]
				operands = operands[1:]
			}
			if len(operands) > 0 {
				scripts = append(scripts, operands[0])
				operands = operands[1:]
			}
		}
		if parsed == nil && o.Regex != nil && !argvFlag(argv, "-f") {
			parsed, _ = sedscript.Parse(context.Background(), strings.Join(scripts, "\n"), argvFlag(argv, "-E"), sedCompiler{o.Regex})
		}
		if parsed != nil {
			for _, path := range parsed.ReadFiles() {
				add(path, false)
			}
			for _, path := range parsed.WriteFiles() {
				add(path, true)
			}
		}
		inPlace := argvFlag(argv, "-i") || argvFlag(argv, "-I")
		suffix := bsdSuffix
		for _, key := range []string{"-i", "-I"} {
			if option, ok := argv.Last(key); ok && len(option.Values) > 0 {
				suffix = option.Values[0].Value
			}
		}
		for _, operand := range operands {
			add(operand, inPlace)
			if inPlace && suffix != "" {
				add(backupName(operand, suffix), true)
			}
		}
	}
	return targets
}
