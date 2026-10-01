package conditions_test

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestScanGatesRequireSourceSnapshotIdentity(t *testing.T) {
	readerErr := errors.New("snapshot unavailable")
	for _, tc := range []struct {
		name       string
		reader     conditions.SourceSnapshotReader
		projectDir string
		want       bool
		wantErr    bool
	}{
		{name: "missing reader", projectDir: "/project", wantErr: true},
		{name: "empty identity", reader: staticSourceSnapshot{}, projectDir: "/project", wantErr: true},
		{name: "reader failure", reader: staticSourceSnapshot{err: readerErr}, projectDir: "/project", wantErr: true},
		{name: "missing project", reader: staticSourceSnapshot{id: "snapshot-1"}, wantErr: true},
		{name: "matching snapshot", reader: staticSourceSnapshot{id: "snapshot-1"}, projectDir: "/project", want: true},
		{name: "other snapshot", reader: staticSourceSnapshot{id: "snapshot-2"}, projectDir: "/project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger := stubScanLedger{complete: make(map[string]*api.CodeScan)}
			for _, categories := range [][]api.ScanCategory{
				{api.ScanCategoryLint}, {api.ScanCategorySecurity, api.ScanCategorySecret},
			} {
				for _, id := range []string{"", "snapshot-1"} {
					ledger.complete[scanKey("dep-1", id, categories)] = &api.CodeScan{
						ID: "scan", Status: api.CodeScanStatusComplete, SourceSnapshotID: id,
					}
				}
			}
			deps := scanDomainDeps(ledger, nil)
			deps.SourceSnapshots = tc.reader
			registry, err := conditions.NewDefaultRegistry(deps)
			testutil.FailErr(t, "build scan conditions", err)
			for _, gate := range []string{"lint_gate_passed", "baseline_scan_complete"} {
				passed, err := registry.Evaluate(gate, conditions.EvalContext{
					Ctx: t.Context(), SessionID: "sess-1", ProjectDir: tc.projectDir,
				})
				if passed != tc.want || (err != nil) != tc.wantErr {
					t.Fatalf("%s = %t, %v; want %t, error %t", gate, passed, err, tc.want, tc.wantErr)
				}
				if tc.name == "reader failure" && !errors.Is(err, readerErr) {
					t.Fatalf("%s lost snapshot error: %v", gate, err)
				}
			}
		})
	}
}
