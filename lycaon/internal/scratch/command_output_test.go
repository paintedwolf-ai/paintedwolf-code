package scratch_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandOutputRejectsInvalidDestinationsWithoutCreatingFiles(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"", ".", "..", "../escape", `..\escape`} {
		if err := scratch.PublishCommandOutput(root, scratch.CommandOutput{ProgramID: id, Stream: "stdout"}, strings.NewReader("bytes")); !errors.Is(err, fseffect.ErrInvalidPath) {
			t.Fatalf("id %q: %v", id, err)
		}
	}
	if err := scratch.PublishCommandOutput(root, scratch.CommandOutput{ProgramID: "program", Stream: "../../escape"}, strings.NewReader("bytes")); !errors.Is(err, fseffect.ErrInvalidPath) {
		t.Fatalf("invalid stream: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid destination created entries: %v, %v", entries, err)
	}
}

func TestCommandOutputNamesBindProgramAndChildStreams(t *testing.T) {
	root := t.TempDir()
	for _, output := range []scratch.CommandOutput{
		{ProgramID: "program", Stream: "stdout"},
		{ProgramID: "program", ExecutionID: "child", Stream: "stderr"},
	} {
		path, err := output.Path()
		testutil.FailErr(t, "output path", err)
		parsed, ok := scratch.ParseCommandOutput(path)
		if !ok || parsed != output {
			t.Fatalf("parsed %q = %+v", path, parsed)
		}
		testutil.FailErr(t, "publish output", scratch.PublishCommandOutput(root, output, strings.NewReader("diagnostic")))
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		testutil.FailErr(t, "read output view", err)
		if string(data) != "diagnostic" {
			t.Fatalf("view = %q", data)
		}
	}
	for _, path := range []string{"../child.stdout", "program/../child.stdout", "program/child/other.stdout", "/child.stdout", "program/child.stderr/", `program\child.stdout`, "program/child.log"} {
		if _, ok := scratch.ParseCommandOutput(path); ok {
			t.Fatalf("accepted invalid output %q", path)
		}
	}
	if err := scratch.PublishCommandOutput(root, scratch.CommandOutput{ProgramID: "program", ExecutionID: "../escape", Stream: "stdout"}, strings.NewReader("bad")); !errors.Is(err, fseffect.ErrInvalidPath) {
		t.Fatalf("invalid child identity: %v", err)
	}
}
