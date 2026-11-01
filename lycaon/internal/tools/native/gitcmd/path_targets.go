package gitcmd

import (
	"path/filepath"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/gitargv"
)

// gitPathTargets follows the already parsed Git subcommand and pathspec
// boundary. Refs, remote names and messages never become file authority.
func gitPathTargets(argv commandprogram.BoundArgv, parsed *command, mode, cwd string) []commandprogram.PathTarget {
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
	directory := cwd
	for _, next := range parsed.dirs {
		if filepath.IsAbs(next) {
			directory = filepath.Clean(next)
		} else {
			directory = filepath.Join(directory, next)
		}
		add(directory, false)
	}
	if len(parsed.dirs) > 0 && mutating {
		add(directory, true)
	}
	addOperand := func(path string, write bool) {
		if path != "" && path != "-" && !filepath.IsAbs(path) {
			path = filepath.Join(directory, path)
		}
		add(path, write)
	}
	operands := argv.Operands().Values()
	switch argv.Command {
	case "git add", "git clean", "git restore":
		for _, path := range operands {
			addOperand(path, argv.Command != "git add")
		}
	case "git checkout":
		if mode == "paths" {
			for _, path := range parsed.afterDash {
				addOperand(path, true)
			}
		}
	case "git worktree":
		if len(operands) > 1 && (operands[0] == "add" || operands[0] == "remove") {
			addOperand(operands[1], true)
		}
	case "git clone":
		if len(operands) > 0 {
			if source, local := gitargv.LocalCloneSourcePath(operands[0]); local {
				addOperand(source, false)
			}
		}
		if len(operands) > 1 {
			addOperand(operands[1], true)
		} else if len(operands) == 1 {
			addOperand(defaultCloneDirName(operands[0]), true)
		}
	case "git reset":
		for _, path := range parsed.afterDash {
			addOperand(path, true)
		}
	case "git diff", "git status", "git ls-files":
		for _, path := range parsed.afterDash {
			addOperand(path, false)
		}
	}
	return targets
}
