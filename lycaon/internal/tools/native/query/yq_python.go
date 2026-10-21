package query

import (
	"bytes"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native"
)

// pythonYqFlags are spellings only python yq (jq over YAML) has. Any of them
// selects that dialect; every other yq invocation is mikefarah yq v4. The
// spellings both share (-e, -n, -r, -i, --indent) mean the same in each.
var pythonYqFlags = []string{
	"-y", "-Y", "--arg", "--argjson", "--slurpfile", "--rawfile", "--args", "--jsonargs",
	"-S", "--tab", "-a", "-c", "-s", "-R", "--raw-output0", "--stream",
}

func (inv *invocation) pythonYq() bool {
	if inv.name() != "yq" {
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
	stdout := inv.out
	defer func() { inv.out = stdout }()
	for _, file := range args.files {
		if file == "-" {
			return inv.usage(file, "a FILE for -i")
		}
		bound, err := inv.paths.Bind(inv.ctx, file, true)
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
		var result bytes.Buffer
		inv.out = &output{ctx: inv.ctx, w: &result, maximum: inv.owners.limit("stream_bytes"), steps: stdout.steps, maxSteps: stdout.maxSteps}
		err = runJqLanguage(inv, jqDialect{yamlInput: true, pythonYAML: true, files: []string{file}})
		stdout.steps = inv.out.steps
		if err != nil || inv.status != 0 {
			return err
		}
		if result.String() == source.text {
			continue
		}
		written, err := inv.owners.Write.Apply(inv.ctx, native.WriteInput{Path: bound.Resolved.Abs, Content: result.String(),
			Admission: native.WriteExecutionPinned, Destination: destination, Sources: []toolcontract.ExecutionRevision{source.revision}}, inv.tc)
		if err != nil {
			return err
		}
		inv.published = inv.published || written.Accepted || written.Saved
		if err := inv.barrier.Track(written); err != nil {
			return err
		}
	}
	return nil
}
