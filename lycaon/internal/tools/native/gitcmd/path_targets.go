package gitcmd

import "github.com/lycaon/lycaon/internal/commandprogram"

// gitPathTargets follows the already parsed Git subcommand and pathspec
// boundary. Refs, remote names and messages never become file authority.
func gitPathTargets(argv commandprogram.BoundArgv, parsed *command, mode string) []commandprogram.PathTarget {
	targets := []commandprogram.PathTarget{}
	_, mutating := mutateForms[argv.Command]
	if argv.Command == "git clean" && mode == "dry-run" || argv.Command == "git stash" && mode == "show" {
		mutating = false
	}
	add := func(path string, write bool) {
		if path != "" && path != "-" {
			targets = append(targets, commandprogram.PathTarget{Path: path, Write: write})
		}
	}
	for _, item := range argv.Items {
		if item.Option != nil && item.Option.Key == "-C" {
			for _, value := range item.Option.Values {
				add(value.Value, mutating)
			}
		}
	}
	operands := argv.Operands().Values()
	switch argv.Command {
	case "git add", "git clean", "git restore":
		for _, path := range operands {
			add(path, argv.Command != "git add")
		}
	case "git checkout":
		if mode == "paths" {
			for _, path := range parsed.afterDash {
				add(path, true)
			}
		}
	case "git worktree":
		if len(operands) > 1 && (operands[0] == "add" || operands[0] == "remove") {
			add(operands[1], true)
		}
	case "git clone":
		if len(operands) > 0 {
			add(operands[0], false)
		}
		if len(operands) > 1 {
			add(operands[1], true)
		} else if len(operands) == 1 {
			add(defaultCloneDirName(operands[0]), true)
		}
	case "git reset":
		for _, path := range parsed.afterDash {
			add(path, true)
		}
	case "git diff", "git status", "git ls-files":
		for _, path := range parsed.afterDash {
			add(path, false)
		}
	}
	return targets
}
