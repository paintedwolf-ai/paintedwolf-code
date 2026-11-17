package contract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

func TestSchemaEnumValuesMatchGo(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "lycaon", "internal", "db", "schema.sql"))
	contractcheck.FailErr(t, "read file", err)
	sql := string(data)

	cases := []struct {
		table  string
		column string
		goVals []string
	}{
		{
			table:  "people",
			column: "role",
			goVals: wirespec.GoEnumValues(api.PersonRoleOwner),
		},
		{
			table:  "sessions",
			column: "posture",
			goVals: wirespec.GoEnumValues(
				api.SessionPostureSpec,
				api.SessionPostureVet,
				api.SessionPostureOrchestrate,
				api.SessionPostureBuild,
			),
		},
		{
			table:  "sessions",
			column: "status",
			goVals: wirespec.GoEnumValues(
				api.SessionStatusPreparing,
				api.SessionStatusIdle,
				api.SessionStatusBusy,
				api.SessionStatusError,
			),
		},
		{
			table:  "messages",
			column: "role",
			goVals: wirespec.GoEnumValues(
				api.MessageRoleUser,
				api.MessageRoleAssistant,
				api.MessageRoleTool,
				api.MessageRoleSystem,
			),
		},
		{
			table:  "worker_jobs",
			column: "status",
			goVals: wirespec.GoEnumValues(
				api.WorkerStatusPending,
				api.WorkerStatusRunning,
				api.WorkerStatusWaiting,
				api.WorkerStatusComplete,
				api.WorkerStatusFailed,
				api.WorkerStatusCanceled,
				api.WorkerStatusHeld,
			),
		},
		{
			table:  "worker_jobs",
			column: "spawn_reason",
			goVals: wirespec.GoEnumValues(
				api.SpawnReasonInitial,
				api.SpawnReasonRetry,
				api.SpawnReasonHumanRequest,
				api.SpawnReasonCloseout,
			),
		},
		{
			table:  "worker_jobs",
			column: "execution_target",
			goVals: wirespec.GoEnumValues(
				api.ExecutionTargetLocal,
				api.ExecutionTargetRunner,
			),
		},
		{
			table:  "code_scans",
			column: "status",
			goVals: wirespec.GoEnumValues(
				api.CodeScanStatusPending,
				api.CodeScanStatusRunning,
				api.CodeScanStatusComplete,
				api.CodeScanStatusFailed,
				api.CodeScanStatusTimedOut,
				api.CodeScanStatusCanceled,
				api.CodeScanStatusSuperseded,
			),
		},
	}

	for _, tc := range cases {
		t.Run(tc.table+"."+tc.column, func(t *testing.T) {
			t.Parallel()
			if !wirespec.SchemaContainsEnumSet(sql, tc.table, tc.column, tc.goVals) {
				t.Fatalf("schema CHECK for %s.%s does not match Go enum %v", tc.table, tc.column, tc.goVals)
			}
		})
	}
}
