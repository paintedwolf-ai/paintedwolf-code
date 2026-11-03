package query

import (
	"github.com/lycaon/lycaon/internal/commandprogram"
)

// pythonYqFlags are spellings only python yq (jq over YAML) has. Any of them
// selects that dialect; every other yq invocation is mikefarah yq v4. The
// spellings both share (-e, -n, -r, -i, --indent) mean the same in each.
var pythonYqFlags = []string{
	"-y", "-Y", "--arg", "--argjson", "--slurpfile", "--rawfile", "--args", "--jsonargs",
	"-S", "--tab", "-a", "-c", "-s", "-R", "--raw-output0", "--stream",
}

func (inv *invocation) pythonYq() bool {
	if inv.spec.Name != "yq" {
		return false
	}
	for _, key := range pythonYqFlags {
		if inv.flag(key) {
			return true
		}
	}
	return false
}

// runPythonYq is python yq: the jq program over each file's YAML documents,
// printing jq's JSON unless -y asks for YAML. -i rewrites each file with
// its own output through the pinned write door.
func runPythonYq(inv *invocation) error {
	if !inv.flag("-i") {
		return runJqLanguage(inv, jqDialect{yamlInput: true, pythonYAML: true})
	}
	args, err := inv.jqArguments()
	if err != nil || args == nil {
		return err
	}
	for _, file := range args.files {
		if err := inv.rewritePythonYq(file); err != nil || inv.status != 0 {
			return err
		}
	}
	return nil
}

// rewritePythonYq holds one file's output through its pinned publication.
func (inv *invocation) rewritePythonYq(file commandprogram.Field) error {
	stdout := inv.out
	defer func() { inv.out = stdout }()
	if file.Value == "-" {
		return inv.usage(file.Value, "a FILE for -i")
	}
	bound, err := inv.paths.Bind(inv.ctx, file.Value, true)
	if err != nil {
		return err
	}
	source, err := inv.captureSource(file)
	if err != nil {
		return err
	}
	destination, err := inv.owners.Revisions.CaptureDestination(inv.ctx, inv.tc, bound.Resolved.Abs)
	if err != nil {
		return err
	}
	if err := inv.recordPin(destination); err != nil {
		return err
	}
	result, err := inv.owners.Revisions.NewBufferedBytes(inv.ctx, inv.owners.limit("whole_file_bytes"), "whole_file_bytes")
	if err != nil {
		return err
	}
	defer result.Close()
	inv.out = &output{ctx: inv.ctx, w: result, maximum: inv.owners.limit("whole_file_bytes"), limitID: "whole_file_bytes"}
	if err := runJqLanguage(inv, jqDialect{yamlInput: true, pythonYAML: true, files: commandprogram.Fields{file}, captured: source}); err != nil || inv.status != 0 {
		return err
	}
	if string(result.Bytes) == source.text {
		return nil
	}
	if err := inv.publishBytes(source, destination, result.Bytes); err != nil {
		return err
	}
	return nil
}
