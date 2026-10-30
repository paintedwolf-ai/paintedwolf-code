package migrations

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type manifestBatchOpcode struct {
	address    int
	opcode     string
	p1, p2, p3 int
	p4         any
}

func TestManifestMigrationBatchBoundsTemporaryState(t *testing.T) {
	// Compare the old single-row form and every supported tail/full batch.
	// The sole ephemeral cursor holds the same three CHECK literals in all cases.
	for count := 1; count <= layoutInsertRows; count++ {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			database, trace := newManifestBatchDatabase(t)
			tx, err := database.BeginTx(t.Context(), nil)
			testutil.FailErr(t, "begin actual target plan", err)
			defer tx.Rollback()
			statement, err := prepareLayoutEntryInsert(t.Context(), tx, count)
			testutil.FailErr(t, "prepare bounded insert", err)
			defer statement.Close()
			// Explain the exact production statement, including target FK/index/trigger DDL.
			rows, err := tx.Query("EXPLAIN "+trace.entrySQL, make([]any, count*8)...)
			testutil.FailErr(t, "explain pinned target insert", err)
			defer rows.Close()
			var program []manifestBatchOpcode
			for rows.Next() {
				var operation manifestBatchOpcode
				var p5 int
				var comment any
				testutil.FailErr(t, "read target VDBE", rows.Scan(&operation.address, &operation.opcode, &operation.p1, &operation.p2, &operation.p3, &operation.p4, &p5, &comment))
				program = append(program, operation)
			}
			testutil.FailErr(t, "read complete target plan", rows.Err())
			assertManifestBatchPlan(t, program, count)
		})
	}
}

func assertManifestBatchPlan(t *testing.T, program []manifestBatchOpcode, count int) {
	t.Helper()
	ephemeral, variables, yields, end := 0, 0, 0, 0
	cursor := -1
	for index, operation := range program {
		switch operation.opcode {
		case "Variable":
			variables++
		case "Yield":
			yields++
		case "EndCoroutine":
			end++
		case "SorterOpen", "OpenAutoindex", "SorterInsert":
			t.Fatalf("insert introduced an unbounded temporary structure: %s", operation.opcode)
		case "OpenWrite":
			if operation.p3 != 0 {
				t.Fatal("insert writes outside private main database")
			}
		case "OpenEphemeral":
			ephemeral++
			cursor = operation.p1
			if operation.p2 != 1 || index+9 >= len(program) {
				t.Fatal("unexpected ephemeral record shape")
			}
			for offset, literal := range []string{"index", "hashed", "stat"} {
				value, record, insert := program[index+1+3*offset], program[index+2+3*offset], program[index+3+3*offset]
				if value.opcode != "String8" || value.p4 != literal || record.opcode != "MakeRecord" || record.p1 != value.p2 || record.p2 != 1 || insert.opcode != "IdxInsert" || insert.p1 != cursor || insert.p2 != record.p3 || insert.p3 != value.p2 {
					t.Fatal("temporary cursor no longer contains only the three constant identity CHECK keys")
				}
			}
		}
	}
	if ephemeral != 1 || variables != count*8 || (count > 1 && (yields != count+1 || end != 1)) {
		t.Fatalf("unexpected insert program bounds: ephemeral=%d variables=%d yields=%d end=%d rows=%d", ephemeral, variables, yields, end, count)
	}
	inserts := 0
	for _, operation := range program {
		if operation.p1 != cursor {
			continue
		}
		switch operation.opcode {
		case "IdxInsert":
			inserts++
		case "Insert", "SorterInsert", "IdxDelete", "Delete", "Clear":
			t.Fatal("unexpected mutation of the constant CHECK cursor")
		}
	}
	if inserts != 3 {
		t.Fatalf("temporary cursor receives %d records, want three constants", inserts)
	}
}
