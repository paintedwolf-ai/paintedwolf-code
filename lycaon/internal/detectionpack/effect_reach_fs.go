package detectionpack

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
)

// Filesystem destination resolution: is every path an action names already
// inside the set the write jail permits? A yes is `local`, which `filter_local`
// tests against. A program this file does not name resolves to `unproven`.

// filesystemTargetImages are programs whose effects target path arguments.
var filesystemTargetImages = map[string]struct{}{
	"rm": {}, "rmdir": {}, "unlink": {}, "shred": {}, "srm": {}, "truncate": {}, "find": {},
	"chmod": {}, "chown": {}, "chgrp": {},
}

// modeFirstImages take a mode or identity operand before the path.
var modeFirstImages = map[string]struct{}{
	"chmod": {}, "chown": {}, "chgrp": {},
}

// gitWorktreeImages bound the git resolver to the same all-stages rule.
var gitWorktreeImages = map[string]struct{}{"git": {}}

// navigationImages change where a stage runs without producing an effect of their
// own, so they never disqualify a command from being classified.
var navigationImages = map[string]struct{}{"cd": {}, "pushd": {}, "popd": {}}

// everyImageIn reports whether every stage presented to this resolver is one it
// understands. Aggregate callers remain conservative for a mixed command;
// matching resolves each host-projected process independently.
func everyImageIn(images []string, allowed map[string]struct{}) bool {
	seen := false
	for _, image := range images {
		if _, ok := navigationImages[image]; ok {
			continue
		}
		if _, ok := allowed[image]; !ok {
			return false
		}
		seen = true
	}
	return seen
}

// resolveFilesystemReach classifies an action whose targets are the paths it names.
// ok is false when this resolver has nothing to say, which leaves the caller at
// unproven rather than asserting containment it did not establish.
func resolveFilesystemReach(images []string, cmd string, args map[string]any, projectDir string, writeRoots []string) (string, bool) {
	if len(writeRoots) == 0 {
		return "", false
	}
	if !everyImageIn(images, filesystemTargetImages) {
		return "", false
	}
	// Every operand of these programs is a path, so bare names count too — that is
	// what `rm -rf node_modules` is. Flag values are included rather than skipped:
	// an extra token can only fail containment, never fake it.
	targets := commandOperands(cmd, true)
	if len(targets) == 0 {
		return "", false
	}
	base := commandBaseDir(args, projectDir)
	for _, target := range targets {
		resolved, ok := resolveTargetPath(target, base)
		if !ok || !confine.PathWithinWriteRoots(resolved, writeRoots) {
			return "", false
		}
	}
	return EffectReachLocal, true
}

// resolveGitWorktreeReach classifies the git subcommands whose destruction lands on
// the working tree rather than on a path in the argv. The tree is `-C <dir>` when
// given and the action's own working directory otherwise.
func resolveGitWorktreeReach(images []string, cmd string, args map[string]any, projectDir string, writeRoots []string) (string, bool) {
	if len(writeRoots) == 0 || !everyImageIn(images, gitWorktreeImages) {
		return "", false
	}
	normalized := NormalizeCommandLine(cmd)
	if !strings.Contains(normalized, " clean ") && !strings.Contains(normalized, " reset ") &&
		!strings.Contains(normalized, " checkout ") && !strings.Contains(normalized, " restore ") {
		return "", false
	}
	base := commandBaseDir(args, projectDir)
	tree := base
	if dir := flagValue(cmd, "-C"); dir != "" {
		resolved, ok := resolveTargetPath(dir, base)
		if !ok {
			return "", false
		}
		tree = resolved
	}
	if tree == "" || !confine.PathWithinWriteRoots(tree, writeRoots) {
		return "", false
	}
	// A path argument can still carry the operation outside the tree
	// (`git -C repo checkout -- ../elsewhere`), so every named path is checked too.
	// Only path-shaped operands here: git's own subcommands and refs are bare words.
	for _, target := range commandOperands(cmd, false) {
		resolved, ok := resolveTargetPath(target, tree)
		if !ok || !confine.PathWithinWriteRoots(resolved, writeRoots) {
			return "", false
		}
	}
	return EffectReachLocal, true
}

// hasParentSegment reports whether any path segment is exactly "..".
func hasParentSegment(target string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(target), "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

// commandBaseDir is the directory relative paths in this action resolve against:
// the action's own cwd when it declared one, the project directory otherwise.
func commandBaseDir(args map[string]any, projectDir string) string {
	cwd, _ := args["cwd"].(string)
	if cwd = strings.TrimSpace(cwd); cwd != "" {
		if filepath.IsAbs(cwd) {
			return cwd
		}
		if projectDir != "" {
			return filepath.Join(projectDir, cwd)
		}
	}
	return projectDir
}

// commandOperands returns the tokens a caller should containment-check.
//
// The leading program name and every flag are dropped. A token carrying a scheme
// is a destination rather than a path and never counts. bare selects whether a
// word with no separator counts: true for programs whose every operand is a path,
// false where bare words are subcommands and refs.
//
// Operands use the wrapper-resolved argv so reach shares its stage boundaries.
func commandOperands(cmd string, bare bool) []string {
	var out []string
	for _, stage := range resolveStages(cmd) {
		argv := stage.argv
		if _, ok := modeFirstImages[stage.image]; ok {
			argv = dropFirstOperand(argv)
		}
		out = append(out, stageOperands(argv, bare)...)
	}
	return out
}

// dropFirstOperand removes the leading non-flag word after the program name.
func dropFirstOperand(argv []string) []string {
	for i := 1; i < len(argv); i++ {
		if strings.HasPrefix(argv[i], "-") {
			continue
		}
		out := append([]string{}, argv[:i]...)
		return append(out, argv[i+1:]...)
	}
	return argv
}

func stageOperands(argv []string, bare bool) []string {
	var out []string
	for i, token := range argv {
		if i == 0 || token == "" || strings.HasPrefix(token, "-") || strings.Contains(token, "://") {
			continue
		}
		pathShaped := strings.HasPrefix(token, "/") || strings.HasPrefix(token, "./") ||
			strings.HasPrefix(token, "../") || strings.HasPrefix(token, "~") ||
			strings.Contains(token, "/")
		if bare || pathShaped {
			out = append(out, token)
		}
	}
	return out
}

// resolveTargetPath makes one target absolute and containment-checkable.
//
// A glob is resolved to the deepest directory its pattern cannot escape: shell
// expansion of `index/localhost-*` can only produce children of `index`, so a
// contained parent contains every expansion. A pattern that also walks upward
// proves nothing and is refused.
func resolveTargetPath(target, base string) (string, bool) {
	target = strings.Trim(strings.TrimSpace(target), `"'`)
	if target == "" {
		return "", false
	}
	if strings.ContainsAny(target, "$`{}") {
		// Dynamic path syntax cannot establish a concrete target.
		return "", false
	}
	if i := strings.IndexAny(target, "*?["); i >= 0 {
		// Clean cannot cancel a ".." against a segment that has not been expanded
		// yet, so `dir/*/../../etc` would collapse to a contained prefix while the
		// shell walks out of it. Any upward step alongside a wildcard is unprovable.
		if hasParentSegment(target) {
			return "", false
		}
		switch cut := strings.LastIndex(target[:i], "/"); {
		case cut > 0:
			target = target[:cut]
		case cut == 0:
			// Preserve the root separator for patterns such as `/*`.
			target = "/"
		default:
			// The pattern names entries of the base directory itself.
			target = "."
		}
	}
	if strings.HasPrefix(target, "~") {
		// Home expansion is the shell's, and its result is outside every root the
		// boundary grants by default; treat it as unresolved rather than guessing.
		return "", false
	}
	if !filepath.IsAbs(target) {
		if base == "" {
			return "", false
		}
		target = filepath.Join(base, target)
	}
	return filepath.Clean(target), true
}
