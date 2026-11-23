package command_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

func TestProgramPublicationRecoversRetainedOutputWithoutReexecution(t *testing.T) {
	ctx := t.Context()
	f := programtest.New(t, programtest.Options{})
	testutil.FailErr(t, "create captured source", os.WriteFile(filepath.Join(f.Dir, "source.txt"), []byte("captured bytes\n"), 0o600))
	_, err := f.Database.ExecContext(ctx, `CREATE TRIGGER reject_program_publication BEFORE UPDATE ON invocation_program_publications
		WHEN NEW.state = 'committed'
		BEGIN SELECT RAISE(ABORT, 'provenance write failed'); END`)
	testutil.FailErr(t, "install publication failure", err)

	result, err := f.Call(t, ctx, "cat source.txt")
	if err == nil || result != "" {
		t.Fatalf("failed publication returned result %q, error %v", result, err)
	}
	var programID string
	err = f.Database.QueryRowContext(ctx, `SELECT id FROM invocation_programs`).Scan(&programID)
	testutil.FailErr(t, "read program identity", err)
	physical := filepath.Join(f.OutputDir, programID+".stdout")
	if _, err := os.Stat(physical); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsettled stream has a public file: %v", err)
	}
	before, err := f.Recorder.ProjectProgram(ctx, f.Session, programID, 1)
	testutil.FailErr(t, "read pending receipt", err)
	if len(before.Program.Outputs) != 0 || len(f.Delivered()) != 0 {
		t.Fatal("unsettled program reports delivery")
	}
	if recovered, err := f.Runtime.ReceiptResult(ctx, f.Session, programID); err == nil || recovered != "" {
		t.Fatalf("unsettled program reports a completed result: %q, %v", recovered, err)
	}

	_, err = f.Database.ExecContext(ctx, `DROP TRIGGER reject_program_publication`)
	testutil.FailErr(t, "remove publication failure", err)
	testutil.FailErr(t, "change source after execution", os.WriteFile(filepath.Join(f.Dir, "source.txt"), []byte("replacement bytes\n"), 0o600))
	resolver := func(_ context.Context, sessionID string) (string, error) {
		if sessionID != f.Session {
			t.Fatalf("publication resolved unexpected session %q", sessionID)
		}
		return f.OutputDir, nil
	}
	restarted := &command.ProgramRuntime{Recorder: f.Recorder, Provenance: f.Runtime.Provenance, Blobs: f.Runtime.Blobs}
	testutil.FailErr(t, "resume output publication", restarted.RecoverProgramPublications(ctx, resolver))
	testutil.FailErr(t, "repeat output reconciliation", restarted.RecoverProgramPublications(ctx, resolver))

	body, err := os.ReadFile(physical)
	testutil.FailErr(t, "read recovered program output", err)
	if string(body) != "captured bytes\n" {
		t.Fatalf("recovered output = %q", body)
	}
	var programs int
	err = f.Database.QueryRowContext(ctx, `SELECT count(*) FROM invocation_programs`).Scan(&programs)
	testutil.FailErr(t, "count program executions", err)
	if programs != 1 {
		t.Fatalf("recovery replayed execution: %d program receipts", programs)
	}
	after, err := f.Recorder.ProjectProgram(ctx, f.Session, programID, 1)
	testutil.FailErr(t, "read recovered receipt", err)
	if len(after.Program.Outputs) != 1 || len(f.Delivered()) != 0 {
		t.Fatal("recovery missed the window or claimed inline delivery")
	}
}
