package search_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/search"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type partialCommandIndexScope struct{}

func (partialCommandIndexScope) Catalog(_ context.Context, root string) *sourcescope.Scope {
	return sourcescope.New(root, sourcescope.Options{Plane: sourcescope.Plane{BoundaryDirectories: []string{"vendor"}}})
}

func TestProgramEntrySearchesIndexedAndLiveSource(t *testing.T) {
	f := programtest.New(t, programtest.Options{})
	for name, body := range map[string]string{
		"src/indexed.go":      "NeedleToken\n",
		"src/noise.go":        "plain source\n",
		"vendor/unindexed.go": "NeedleToken\n",
	} {
		abs := filepath.Join(f.Dir, filepath.FromSlash(name))
		testutil.FailErr(t, "create source directory", os.MkdirAll(filepath.Dir(abs), 0o700))
		testutil.FailErr(t, "write source", os.WriteFile(abs, []byte(body), 0o600))
	}
	catalog := sourcecatalog.New()
	catalog.SetScopes(partialCommandIndexScope{})
	t.Cleanup(func() { testutil.FailErr(t, "drain command source catalog", catalog.Drain(context.Background())) })
	_, err := catalog.Observe(t.Context(), "project", []sourcecatalog.Root{{ID: "root", Path: f.Dir}})
	testutil.FailErr(t, "observe partial index", err)
	searches, err := search.NewOwners(native.ExecutionRevisions{Boundary: f.Runtime.Boundary}, f.Recorder, nil)
	testutil.FailErr(t, "prepare search operations", err)
	searches.Catalog = catalog
	f.Runtime.Owners = func(_ tools.ToolContext, _ *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, admission tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		programSearch := *searches
		programSearch.Revisions.Memory = memory
		programSearch.Admission = admission
		searches := &programSearch
		natives := map[string]command.NativeOperation{}
		for _, name := range searches.Commands() {
			tool, _ := searches.Operation(name)
			natives[name] = command.NativeOperation{Tool: tool, Prepare: func(_ context.Context, paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, err := searches.Prepare(paths, dispatch, request)
				if err != nil {
					return command.NativePrepared{}, err
				}
				return command.NativePrepared{Mode: search.EffectMode(prepared.Argv), Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
			}}
		}
		return command.ProgramOwners{Natives: natives}, nil
	}
	check := func(want ...string) {
		t.Helper()
		out, err := f.Call(t, t.Context(), "grep -rl NeedleToken .")
		testutil.FailErr(t, "run command search", err)
		var result struct {
			Stdout string `json:"stdout"`
			Status int    `json:"status"`
		}
		testutil.FailErr(t, "decode command result", json.Unmarshal([]byte(out), &result))
		lines := strings.Fields(result.Stdout)
		if result.Status != 0 || len(lines) != len(want) {
			t.Fatalf("command search = %+v, want %v", result, want)
		}
		for _, path := range want {
			if !strings.Contains(result.Stdout, path) {
				t.Fatalf("command search = %+v, missing %s", result, path)
			}
		}
	}
	check("src/indexed.go", "vendor/unindexed.go")
	check("src/indexed.go", "vendor/unindexed.go")
	changed := filepath.Join(f.Dir, "src", "noise.go")
	testutil.FailErr(t, "change indexed file", os.WriteFile(changed, []byte("NeedleToken after indexing\n"), 0o600))
	check("src/indexed.go", "src/noise.go", "vendor/unindexed.go")
	find := func(want ...string) {
		t.Helper()
		out, err := f.Call(t, t.Context(), "find . -name '*.go'")
		testutil.FailErr(t, "run command find", err)
		var result struct {
			Stdout string `json:"stdout"`
			Status int    `json:"status"`
		}
		testutil.FailErr(t, "decode command find result", json.Unmarshal([]byte(out), &result))
		lines := strings.Fields(result.Stdout)
		if result.Status != 0 || len(lines) != len(want) {
			t.Fatalf("command find = %+v, want %v", result, want)
		}
		for _, path := range want {
			if !strings.Contains(result.Stdout, path) {
				t.Fatalf("command find = %+v, missing %s", result, path)
			}
		}
	}
	find("src/indexed.go", "src/noise.go", "vendor/unindexed.go")
	testutil.FailErr(t, "add file after indexing", os.WriteFile(filepath.Join(f.Dir, "src", "added.go"), []byte("NeedleToken newly added\n"), 0o600))
	find("src/indexed.go", "src/noise.go", "src/added.go", "vendor/unindexed.go")
	check("src/indexed.go", "src/noise.go", "src/added.go", "vendor/unindexed.go")
	testutil.FailErr(t, "delete indexed file", os.Remove(filepath.Join(f.Dir, "src", "indexed.go")))
	find("src/noise.go", "src/added.go", "vendor/unindexed.go")
	check("src/noise.go", "src/added.go", "vendor/unindexed.go")
}
