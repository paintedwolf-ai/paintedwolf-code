package command_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type memorySourceCapture struct {
	files map[string]string
}

func (m memorySourceCapture) OpenInput(context.Context, commandprogram.InputDestination) (io.ReadCloser, error) {
	return nil, fmt.Errorf("input redirection unused")
}

func (m memorySourceCapture) Capture(_ context.Context, _ toolcontract.OperationInvocation, req commandprogram.SourceCaptureRequest) (commandprogram.CapturedSource, error) {
	body, ok := m.files[req.Path]
	if !ok {
		return commandprogram.CapturedSource{}, fmt.Errorf("source %q not found", req.Path)
	}
	raw := []byte(body)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	return commandprogram.CapturedSource{
		Pin: toolcontract.SourcePin{
			SourceID:       req.Path,
			Revision:       digest,
			Representation: toolcontract.UTF8SourceBytes,
			SHA256:         digest,
			EndByte:        int64(len(raw)),
		},
		Bytes: raw,
	}, nil
}

func echoTextOperation(name string) command.NativeOperation {
	return command.NativeOperation{
		Tool: "read",
		Prepare: func(_ context.Context, _ *projectpaths.ProgramPaths, _ commandprogram.Dispatch, req commandprogram.OperationRequest) (command.NativePrepared, error) {
			bound, err := commandprogram.CatalogBinder{}.Bind(catalogSpec(name), req.Argv)
			if err != nil {
				return command.NativePrepared{}, err
			}
			text := strings.Join(req.Argv.Values()[1:], " ")
			text = strings.ReplaceAll(text, `\n`, "\n")
			return command.NativePrepared{
				Review: bound.Review("read"),
				Invoke: func(_ context.Context, _ tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
					_, err := io.WriteString(streams.Output, text)
					if err != nil {
						return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
					}
					return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
				},
			}, nil
		},
	}
}

func newSourceDedupFixture(t *testing.T, files map[string]string) *programtest.Fixture {
	t.Helper()
	return programtest.New(t, programtest.Options{
		Input: func(string) commandprogram.InputRedirection { return memorySourceCapture{files: files} },
		Natives: func(string) map[string]command.NativeOperation {
			return map[string]command.NativeOperation{
				"printf": echoTextOperation("printf"),
				"echo":   echoTextOperation("echo"),
			}
		},
	})
}

func TestProgramSourceRecordsSingleExecutionOnRefusalAndSuccess(t *testing.T) {
	t.Run("syntax_refusal", func(t *testing.T) {
		f := newSourceDedupFixture(t, map[string]string{"./bad.sh": "echo inner\ntrap x INT\n"})
		raw, err := f.Call(t, t.Context(), "printf 'parent\\n'; source -- ./bad.sh; printf 'after\\n'")
		testutil.FailErr(t, "run source syntax refusal", err)

		var outcome command.ProgramOutcome
		testutil.FailErr(t, "decode refusal outcome", json.Unmarshal([]byte(raw), &outcome))
		if outcome.State != "rejected" || outcome.Refusal == nil || outcome.Refusal.Code != "COMMAND_SYNTAX_UNSUPPORTED" {
			t.Fatalf("outcome = %+v, want rejected COMMAND_SYNTAX_UNSUPPORTED", outcome)
		}
		if outcome.Stdout != "parent\n" {
			t.Fatalf("stdout = %q, want parent\\n", outcome.Stdout)
		}
		if strings.TrimSpace(outcome.Stderr) == "" {
			t.Fatal("expected diagnostic stderr on source syntax refusal")
		}

		page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, outcome.ProgramID, "", 50)
		testutil.FailErr(t, "read program receipts", err)
		var printfCount, sourceCount, echoCount int
		for _, exec := range page.Executions {
			switch exec.NativeCommand {
			case "printf":
				printfCount++
				if exec.State != "completed" || exec.StatusCode == nil || *exec.StatusCode != 0 {
					t.Fatalf("unexpected printf execution: %+v", exec)
				}
			case "source":
				sourceCount++
				if exec.State != "refused" || exec.StatusCode == nil || *exec.StatusCode != 2 {
					t.Fatalf("unexpected source execution: %+v", exec)
				}
			case "echo":
				echoCount++
			}
		}
		if printfCount != 1 || sourceCount != 1 || echoCount != 0 {
			t.Fatalf("execution counts printf=%d source=%d echo=%d, want 1/1/0; all=%+v", printfCount, sourceCount, echoCount, page.Executions)
		}
	})

	t.Run("success", func(t *testing.T) {
		f := newSourceDedupFixture(t, map[string]string{"./good.sh": "printf 'inner\\n'\n"})
		raw, err := f.Call(t, t.Context(), "printf 'parent\\n'; source -- ./good.sh; printf 'after\\n'")
		testutil.FailErr(t, "run valid source", err)

		var outcome command.ProgramOutcome
		testutil.FailErr(t, "decode success outcome", json.Unmarshal([]byte(raw), &outcome))
		if outcome.State != "completed" || outcome.Status != 0 || outcome.Stdout != "parent\ninner\nafter\n" {
			t.Fatalf("outcome = %+v", outcome)
		}

		page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, outcome.ProgramID, "", 50)
		testutil.FailErr(t, "read program receipts", err)
		var printfCount, sourceCount int
		for _, exec := range page.Executions {
			switch exec.NativeCommand {
			case "printf":
				printfCount++
			case "source":
				sourceCount++
				if exec.State != "completed" || exec.StatusCode == nil || *exec.StatusCode != 0 {
					t.Fatalf("unexpected source execution: %+v", exec)
				}
			}
		}
		if printfCount != 3 || sourceCount != 1 {
			t.Fatalf("execution counts printf=%d source=%d, want 3/1; all=%+v", printfCount, sourceCount, page.Executions)
		}
		var sourceRows int
		testutil.FailErr(t, "count registered source units", f.Database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_sources WHERE program_id=? AND source_unit_id='./good.sh'`, outcome.ProgramID).Scan(&sourceRows))
		if sourceRows != 1 {
			t.Fatalf("registered source rows for ./good.sh = %d, want 1", sourceRows)
		}
	})
}
