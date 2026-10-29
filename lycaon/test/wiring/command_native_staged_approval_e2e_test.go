package wiring

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSedStagedApprovalRetainsFinalWriteAuthorityAndInputs(t *testing.T) {
	for _, posture := range []gate.Posture{gate.PostureLight, gate.PostureBalanced, gate.PostureStrict} {
		for _, option := range []string{"approve_current_action", "recommended"} {
			t.Run(string(posture)+"/"+option, func(t *testing.T) {
				script := filepath.Join(outsideCommandFolder(t), "write.sed")
				written := filepath.Join(outsideCommandFolder(t), "written.txt")
				testutil.FailErr(t, "seed staged script", os.WriteFile(script, []byte("w "+written+"\n"), 0o600))
				s := newCommandProgramSession(t, "sed -f '"+script+"' notes.txt", "", posture)
				cards := runCommandWithApprovals(t, s, option, option)
				if len(cards) != 2 || !slices.Contains(cards[0].ToolApproval.Plan.Reasons, wire.GateOutsideRootsRead) || !slices.Contains(cards[1].ToolApproval.Plan.Reasons, wire.GateOutsideRootsWrite) {
					t.Fatalf("staged source/effect disclosure = %+v", cards)
				}
				if got := s.outcome(t, "program-1"); got.Status != 0 {
					t.Fatalf("approved staged write = %+v", got)
				}
				data, err := os.ReadFile(written)
				testutil.FailErr(t, "read staged write", err)
				if string(data) != "first note\nsecond note\n" {
					t.Fatalf("staged write = %q", data)
				}
				if option == "approve_current_action" {
					for path, mode := range map[string]grantedpath.Mode{script: grantedpath.ModeRead, written: grantedpath.ModeWrite} {
						if grant, ok := s.h.Sessions.GrantedPath.Covers(s.session, "", path, mode); ok {
							t.Fatalf("current-action approval installed reusable authority: %+v", grant)
						}
					}
				}
				assertStagedSedReviewedInputs(t, s, script, written)
			})
		}
	}
}

func assertStagedSedReviewedInputs(t *testing.T, s *commandProgramSession, script, written string) {
	t.Helper()
	var mode, certainty, initialDigest, finalDigest, representation, blobJSON string
	var started, pins int
	err := s.h.DB.QueryRowContext(t.Context(), `SELECT e.effect_mode,e.effect_certainty,e.owner_started,e.arguments_digest,
		e.reviewed_arguments_digest,e.reviewed_arguments_representation,o.blob_json,
		(SELECT count(*) FROM invocation_program_revision_pins pin WHERE pin.execution_id=e.id AND pin.path=?)
		FROM invocation_program_executions e JOIN invocation_programs p ON p.id=e.program_id
		JOIN invocation_receipts r ON r.id=p.parent_receipt_id
		JOIN invocation_program_outputs o ON o.program_id=e.program_id AND o.execution_id=e.id AND o.reference=e.reviewed_arguments_ref
		WHERE r.session_id=? AND e.operation_id='sed' AND e.native_command='sed' AND o.stream='arguments'`, script, s.session).
		Scan(&mode, &certainty, &started, &initialDigest, &finalDigest, &representation, &blobJSON, &pins)
	testutil.FailErr(t, "read final staged receipt provenance", err)
	if mode != "w-command" || certainty != "completed" || started != 1 || pins != 1 || initialDigest == finalDigest {
		t.Fatalf("staged receipt mode=%s certainty=%s started=%d pins=%d initial=%s final=%s", mode, certainty, started, pins, initialDigest, finalDigest)
	}
	var databasePath string
	testutil.FailErr(t, "locate fixture host storage", s.h.DB.QueryRowContext(t.Context(), `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&databasePath))
	var blob blobstore.Blob
	testutil.FailErr(t, "decode retained input blob", json.Unmarshal([]byte(blobJSON), &blob))
	reader, err := (blobstore.Store{Root: filepath.Dir(databasePath), Dir: tooloutput.ToolOutputSpillDir}).Open(blob)
	testutil.FailErr(t, "open retained final inputs", err)
	body, err := io.ReadAll(reader)
	testutil.FailErr(t, "read retained final inputs", err)
	testutil.FailErr(t, "close retained final inputs", reader.Close())
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1 << 20, State: 1 << 20})
	testutil.FailErr(t, "admit fixture input inspection", err)
	reviewed, releaseReview, err := toolcontract.RestoreReviewedOperationInputs(t.Context(), memory, representation, finalDigest, string(body))
	testutil.FailErr(t, "verify retained final input digest", err)
	defer releaseReview()
	arguments, releaseArguments, err := reviewed.Arguments(t.Context(), memory)
	testutil.FailErr(t, "decode retained final inputs", err)
	defer releaseArguments()
	targets, ok := arguments["path_targets"].(map[string]any)
	reads, readsOK := targets["read"].([]any)
	if !ok || !readsOK || !reflect.DeepEqual(targets["write"], []any{written}) || !slices.Contains(reads, any(script)) {
		t.Fatalf("receipt omitted disclosed target: %+v", arguments)
	}
}
