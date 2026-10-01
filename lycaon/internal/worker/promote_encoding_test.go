package worker_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerPromoteRoundTripsSelfIdentifyingEncodings(t *testing.T) {
	for _, encoding := range testutil.SelfIdentifyingTextEncodings() {
		t.Run(encoding, func(t *testing.T) {
			primary := t.TempDir()
			branch := t.TempDir()
			path := "notes.txt"
			before := testutil.EncodeTextFixture(t, "before 世界\n", encoding)
			after := testutil.EncodeTextFixture(t, "after 世界\n", encoding)
			testutil.FailErr(t, "write primary", os.WriteFile(filepath.Join(primary, path), before, 0o600))
			baseline := testbaseline.Capture(t, primary)
			reader, err := workspacebaseline.Open(t.Context(), baseline, workspacebaseline.ContentStore(baseline))
			testutil.FailErr(t, "open baseline", err)
			content, exists, err := reader.Content(t.Context(), path)
			testutil.FailErr(t, "decode baseline", err)
			testutil.FailErr(t, "close baseline", reader.Close())
			if !exists || content != "before 世界\n" {
				t.Fatalf("baseline=%q exists=%v", content, exists)
			}
			testutil.FailErr(t, "write branch", os.WriteFile(filepath.Join(branch, path), after, 0o600))

			task := promoteTask(t, primary, branch, nil, []string{path})
			task.WorkspaceBaselinePath = baseline
			assessment, err := worker.AssessPromotePaths3Way(
				context.Background(), nil, "sess-1", task, []string{path},
			)
			testutil.FailErr(t, "assess promote", err)
			if len(assessment.Conflicts) != 0 || len(assessment.MergeResults) != 1 {
				t.Fatalf("assessment=%+v", assessment)
			}
			result := assessment.MergeResults[0]
			if result.Content != "after 世界\n" || result.Encoding != encoding {
				t.Fatalf("result=%+v", result)
			}
			promote := worker.PromoteRootsForTask(task, nil)
			testutil.FailErr(t, "write promoted text",
				promote.WritePrimaryText(task, path, result.Content, result.Encoding))
			got, err := os.ReadFile(filepath.Join(primary, path))
			testutil.FailErr(t, "read promoted bytes", err)
			if !bytes.Equal(got, after) {
				t.Fatalf("promoted bytes=%x want=%x", got, after)
			}
		})
	}
}

func TestWorkerPromoteRefusesEncodingChangesWithoutWriting(t *testing.T) {
	primary := t.TempDir()
	branch := t.TempDir()
	path := "notes.txt"
	before := testutil.EncodeTextFixture(t, "before\n", textfile.UTF16LEBOM)
	after := testutil.EncodeTextFixture(t, "after\n", textfile.UTF8)
	testutil.FailErr(t, "write primary", os.WriteFile(filepath.Join(primary, path), before, 0o600))
	baseline := testbaseline.Capture(t, primary)
	testutil.FailErr(t, "write branch", os.WriteFile(filepath.Join(branch, path), after, 0o600))

	task := promoteTask(t, primary, branch, nil, []string{path})
	task.WorkspaceBaselinePath = baseline
	assessment, err := worker.AssessPromotePaths3Way(
		context.Background(), nil, "sess-1", task, []string{path},
	)
	testutil.FailErr(t, "assess promote", err)
	if len(assessment.Conflicts) != 1 || len(assessment.MergeResults) != 0 {
		t.Fatalf("assessment=%+v", assessment)
	}
	if assessment.Conflicts[0].Reason != api.WorkerPromoteReasonEncodingChanged {
		t.Fatalf("reason=%q", assessment.Conflicts[0].Reason)
	}
	got, err := os.ReadFile(filepath.Join(primary, path))
	testutil.FailErr(t, "read unchanged primary", err)
	if !bytes.Equal(got, before) {
		t.Fatalf("primary changed: got=%x want=%x", got, before)
	}
}
