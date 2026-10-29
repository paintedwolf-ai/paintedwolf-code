package gitcmd

import (
	"strconv"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

var mutateForms = map[string]operandRule{
	"git init": initOperands,
	"git add":  addOperands, "git commit": commitOperands, "git restore": restoreOperands,
	"git checkout": checkoutOperands, "git switch": switchOperands, "git merge": mergeOperands,
	"git cherry-pick": sequenceOperands, "git revert": sequenceOperands, "git rebase": rebaseOperands,
	"git stash": stashOperands, "git reset": resetOperands, "git clean": cleanOperands,
	"git worktree": worktreeOperands, "git fetch": anyOperands, "git pull": pullOperands,
	"git push": anyOperands, "git ls-remote": anyOperands, "git clone": cloneOperands,
}

func initOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	if len(bound.Operands()) > 1 {
		return operandInvalid(spec, bound.Operands()[1].Value, "at most one directory")
	}
	return nil
}

func addOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	if has(bound, "-A") && has(bound, "-u") {
		return operandInvalid(spec, "-u", "either -A or -u, not both")
	}
	if !has(bound, "-A") && !has(bound, "-u") && len(bound.Operands()) == 0 {
		return operandInvalid(spec, spec.Name, "a PATH, -A or -u")
	}
	return nil
}

func commitOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	if len(bound.Options("-m")) == 0 && !has(bound, "--no-edit") {
		return operandInvalid(spec, spec.Name, "-m MESSAGE or --no-edit")
	}
	if has(bound, "-a") && len(bound.Operands()) > 0 {
		return operandInvalid(spec, "-a", "cannot combine -a with explicit paths")
	}
	return nil
}

func restoreOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	if len(bound.Operands()) == 0 {
		return operandInvalid(spec, spec.Name, "at least one PATH")
	}
	return nil
}

func checkoutOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	if has(bound, "-b") && has(bound, "--detach") {
		return operandInvalid(spec, "--detach", "either -b BRANCH or --detach, not both")
	}
	if has(bound, "-b") || has(bound, "--detach") {
		if bound.EndOfOptions || len(bound.Operands()) > 1 {
			return operandInvalid(spec, spec.Name, "at most one starting REF without --")
		}
		return nil
	}
	if len(bound.Operands()) == 0 {
		return operandInvalid(spec, spec.Name, "a BRANCH, REF or PATH")
	}
	return nil
}

func switchOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	if has(bound, "-c") && has(bound, "--detach") {
		return operandInvalid(spec, "-d", "either -c BRANCH or -d, not both")
	}
	if has(bound, "-c") || has(bound, "--detach") {
		if len(bound.Operands()) > 1 {
			return operandInvalid(spec, bound.Operands()[1].Value, "at most one starting REF")
		}
		return nil
	}
	if len(bound.Operands()) != 1 {
		return operandInvalid(spec, spec.Name, "a BRANCH")
	}
	return nil
}

func mergeOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	if has(bound, "--continue") && has(bound, "--abort") {
		return operandInvalid(spec, "--abort", "either --continue or --abort, not both")
	}
	modes := countFlags(bound, "--ff", "--no-ff", "--ff-only")
	if modes > 1 {
		return operandInvalid(spec, spec.Name, "at most one merge fast-forward mode")
	}
	if has(bound, "--continue") || has(bound, "--abort") {
		if len(bound.Operands()) > 0 || modes > 0 || has(bound, "--no-commit") {
			return operandInvalid(spec, spec.Name, "--continue or --abort without REF or mode flags")
		}
		return nil
	}
	if len(bound.Operands()) != 1 {
		return operandInvalid(spec, spec.Name, "one REF")
	}
	return nil
}

func sequenceOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	actions := countFlags(bound, "--continue", "--abort", "--skip")
	if actions > 1 {
		return operandInvalid(spec, spec.Name, "at most one of --continue, --abort or --skip")
	}
	if actions == 1 {
		if len(bound.Operands()) > 0 || has(bound, "-n") {
			return operandInvalid(spec, spec.Name, "--continue, --abort or --skip without REF operands")
		}
		return nil
	}
	if len(bound.Operands()) == 0 {
		return operandInvalid(spec, spec.Name, "at least one REF")
	}
	return nil
}

func rebaseOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	actions := countFlags(bound, "--continue", "--abort", "--skip")
	if actions > 1 {
		return operandInvalid(spec, spec.Name, "at most one of --continue, --abort or --skip")
	}
	if actions == 1 {
		if len(bound.Operands()) > 0 {
			return operandInvalid(spec, spec.Name, "--continue, --abort or --skip without UPSTREAM")
		}
		return nil
	}
	if len(bound.Operands()) != 1 {
		return operandInvalid(spec, spec.Name, "an UPSTREAM ref")
	}
	return nil
}

func stashOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	operands := bound.Operands().Values()
	if len(operands) == 0 {
		if has(bound, "--index") {
			return operandInvalid(spec, "--index", "--index only with apply or pop")
		}
		return nil
	}
	sub := operands[0]
	switch sub {
	case "push", "save":
		if has(bound, "--index") {
			return operandInvalid(spec, "--index", "--index only with apply or pop")
		}
	case "apply", "pop":
		if has(bound, "-m") || has(bound, "-u") || len(operands) > 2 {
			return operandInvalid(spec, sub, sub+" [--index] [-q] [ID]")
		}
	case "drop", "show":
		if has(bound, "-m") || has(bound, "-u") || has(bound, "--index") || len(operands) > 2 {
			return operandInvalid(spec, sub, sub+" [ID]")
		}
	default:
		if !bound.EndOfOptions {
			return operandInvalid(spec, sub, "push, save, apply, pop, drop or show")
		}
	}
	return nil
}

func resetOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	if countFlags(bound, "--soft", "--mixed", "--hard", "--keep", "--merge") > 1 {
		return operandInvalid(spec, spec.Name, "at most one reset mode")
	}
	return nil
}

func cleanOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	if has(bound, "-f") == has(bound, "-n") {
		return operandInvalid(spec, spec.Name, "exactly one of -f or -n")
	}
	return nil
}

func worktreeOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	operands := bound.Operands().Values()
	if len(operands) == 0 {
		return operandInvalid(spec, spec.Name, "add, remove or prune")
	}
	switch operands[0] {
	case "add":
		if len(operands) < 2 || len(operands) > 3 {
			return operandInvalid(spec, "add", "add [-b BRANCH] PATH [REF]")
		}
	case "remove":
		if len(operands) != 2 || has(bound, "-b") {
			return operandInvalid(spec, "remove", "remove PATH")
		}
	case "prune":
		if len(operands) != 1 || has(bound, "-b") {
			return operandInvalid(spec, "prune", "prune")
		}
	default:
		return operandInvalid(spec, operands[0], "add, remove or prune")
	}
	return nil
}

func pullOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	if has(bound, "--ff-only") && has(bound, "--rebase") {
		return operandInvalid(spec, "--rebase", "either --ff-only or --rebase, not both")
	}
	if len(bound.Operands()) > 2 {
		return operandInvalid(spec, bound.Operands()[2].Value, "at most REMOTE and REFSPEC")
	}
	return nil
}

func cloneOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	if n := len(bound.Operands()); n < 1 || n > 2 {
		return operandInvalid(spec, spec.Name, "URL [DIR]")
	}
	if depth, ok := optionValue(bound, "--depth"); ok {
		if n, err := strconv.Atoi(depth); err != nil || n <= 0 {
			return operandInvalid(spec, depth, "a positive depth integer")
		}
	}
	return nil
}

func effectMode(spec toolcontract.CommandSpecification, c *command, bound commandprogram.BoundArgv) string {
	switch spec.Name {
	case "git restore":
		if has(bound, "-S") && !has(bound, "-W") {
			return "staged-only"
		}
		return "worktree"
	case "git checkout":
		if !has(bound, "-b") && !has(bound, "--detach") && (len(c.afterDash) > 0 || (len(c.beforeDash) == 1 && c.beforeDash[0] == ".")) {
			return "paths"
		}
		return "switch-or-create"
	case "git stash":
		if len(c.beforeDash) > 0 {
			switch c.beforeDash[0] {
			case "show":
				return "show"
			case "drop":
				return "drop"
			case "apply", "pop":
				return "apply-pop"
			}
		}
		return "push-save"
	case "git reset":
		if has(bound, "--hard") {
			return "hard"
		}
		return "soft-or-mixed"
	case "git clean":
		if has(bound, "-n") {
			return "dry-run"
		}
		return "force"
	case "git worktree":
		if ops := bound.Operands().Values(); len(ops) > 0 && ops[0] == "add" {
			return "add"
		}
		return "remove-or-prune"
	case "git push":
		if has(bound, "--delete") || has(bound, "--force-with-lease") {
			return "delete-or-lease"
		}
		return "push"
	}
	if len(spec.Effects) > 0 {
		return spec.Effects[0].Mode
	}
	return ""
}

func splitDashOperands(spec toolcontract.CommandSpecification, args []string, bound commandprogram.BoundArgv) ([]string, []string) {
	all := bound.Operands().Values()
	if !bound.EndOfOptions {
		return all, nil
	}
	valueFlag := map[string]bool{}
	for _, opt := range spec.Forms {
		if opt.Scope != "global" && opt.Arity == 1 {
			for _, s := range opt.Spellings {
				valueFlag[s] = true
			}
		}
	}
	for idx := 1; idx < len(args); idx++ {
		if args[idx] == "--" {
			after := append([]string{}, args[idx+1:]...)
			if len(after) > len(all) {
				return nil, all
			}
			return all[:len(all)-len(after)], after
		}
		if valueFlag[args[idx]] {
			idx++
		}
	}
	return all, nil
}
