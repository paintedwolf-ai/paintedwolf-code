package mutation

import (
	"os"
	"path"
	"path/filepath"
	"strconv"

	"github.com/lycaon/lycaon/internal/tools/native/patchapply"
)

// count reads a non-negative integer option value.
func (inv *invocation) count(key string) (int, bool, error) {
	raw, ok := inv.value(key)
	if !ok {
		return 0, false, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, true, inv.usage(raw, "a non-negative number for "+key)
	}
	return n, true, nil
}

// inDir places a relative name under -d DIR, the way patch changes
// directory before reading anything.
func inDir(dir, name string) string {
	if dir == "" || name == "" || filepath.IsAbs(name) || path.IsAbs(name) {
		return name
	}
	return path.Join(dir, name)
}

// patchInput reads the patch from -i FILE, the PATCHFILE operand, or stdin.
// A file input is pinned like any other captured source.
func (inv *invocation) patchInput(dir string, operands []string) ([]byte, error) {
	name, ok := inv.value("-i")
	if !ok && len(operands) == 2 {
		name, ok = operands[1], true
	}
	if !ok || name == "-" {
		return inv.stdinBounded("stream_bytes")
	}
	bound, err := inv.bind(inDir(dir, name), false)
	if err != nil {
		return nil, err
	}
	return inv.owners.CapturePatchInput(inv.ctx, inv.tc, inv.request.Invocation.Identity.ExecutionID, bound.Resolved.Abs)
}

// bindPatchSection resolves a section's names: ORIGFILE when given, the
// names with -p N components removed, or with no -p a Git section's names
// past a/ and b/ and a traditional name as written when its directory
// exists, otherwise its base name (GNU's default).
func (inv *invocation) bindPatchSection(file patchapply.File, dir string, operands []string, strip int, stripSet bool) (*patchSection, error) {
	if !stripSet && file.Git {
		strip = 1
	}
	oldName, newName, err := file.Names(strip)
	if err != nil {
		return nil, inv.usage(file.NewName, "a name with enough components for -p")
	}
	if !stripSet && !file.Git {
		oldName, newName = inv.defaultPatchName(dir, oldName), inv.defaultPatchName(dir, newName)
	}
	if len(operands) > 0 {
		if oldName != "" {
			oldName = operands[0]
		}
		if newName != "" {
			newName = operands[0]
		}
	}
	section := &patchSection{file: file, display: newName}
	if section.display == "" {
		section.display = oldName
	}
	for _, name := range []*string{&oldName, &newName} {
		if *name == "" {
			continue
		}
		bound, err := inv.bind(inDir(dir, *name), true)
		if err != nil {
			return nil, err
		}
		*name = bound.Resolved.Abs
	}
	if newName == "" {
		// A deletion that leaves content behind rewrites the file instead.
		newName = oldName
	}
	section.target = PatchTarget{OldAbs: oldName, NewAbs: newName}
	return section, nil
}

func (inv *invocation) defaultPatchName(dir, name string) string {
	if name == "" || path.IsAbs(name) {
		return name
	}
	parent := path.Dir(name)
	if parent == "." {
		return name
	}
	if bound, err := inv.paths.Bind(inv.ctx, inDir(dir, parent), false); err == nil {
		if info, statErr := os.Stat(bound.Resolved.Abs); statErr == nil && info.IsDir() {
			return name
		}
	}
	return path.Base(name)
}
