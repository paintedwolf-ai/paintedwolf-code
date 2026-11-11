package hitl_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestNativeAuthorityReplayScratchIsIndependentOfRecordCount(t *testing.T) {
	host := startNativeAuthorityHost(t, filepath.Join(t.TempDir(), "store.db"))
	ctx, checkpointID, plan := mintDirectoryApproval(t, host, productionAuthorityMemory(t))
	installer := &nativeInstallRecorder{}
	host.manager.Authority.SetApprovalAuthorityInstaller(installer)
	optionID := ""
	for _, option := range plan.Options {
		if option.Rung == hitl.ApprovalRungChat && !option.Disabled {
			optionID = option.ID
			break
		}
	}
	if optionID == "" {
		t.Fatal("directory approval has no chat choice")
	}
	_, err := host.manager.Authority.ResolveApprovalOption(ctx, "session", checkpointID, optionID)
	testutil.FailErr(t, "approve retained directory authority", err)
	one := productionAuthorityMemory(t)
	host.manager.Authority.SetNativeAuthorityAdmission(one)
	testutil.FailErr(t, "replay one retained grant", host.manager.Authority.RestoreChatGrants(ctx))
	var id, delta, createdAt, format, body string
	testutil.FailErr(t, "read retained authority template", host.database.QueryRowContext(ctx, `SELECT id, delta_json, created_at, authority_format, authority_body FROM chat_grants`).Scan(&id, &delta, &createdAt, &format, &body))
	const records = 256
	for n := range records {
		nextID := fmt.Sprintf("grant_fixture_%08d", n)
		nextBody := strings.ReplaceAll(body, id, nextID)
		sum := sha256.Sum256([]byte(nextBody))
		_, err := host.database.ExecContext(ctx, `INSERT INTO chat_grants(chat_session_id,id,checkpoint_id,delta_json,created_at,authority_format,authority_digest,authority_body) VALUES('session',?,?,?,?,?,?,?)`, nextID, checkpointID, strings.ReplaceAll(delta, id, nextID), createdAt, format, hex.EncodeToString(sum[:]), nextBody)
		testutil.FailErr(t, "seed retained authority", err)
	}
	many := productionAuthorityMemory(t)
	host.manager.Authority.SetNativeAuthorityAdmission(many)
	installer.installed = nil
	testutil.FailErr(t, "replay retained authority records", host.manager.Authority.RestoreChatGrants(ctx))
	if len(installer.installed) != records+1 {
		t.Fatalf("replayed %d records, want %d", len(installer.installed), records+1)
	}
	if many.InUse().State != 0 || many.Peak().State > one.Peak().State+4096 {
		t.Fatalf("replay scratch grew with records: one=%+v many=%+v held=%+v", one.Peak(), many.Peak(), many.InUse())
	}

	var optionJSON, operationBody, operationFormat string
	testutil.FailErr(t, "read prepared authority template", host.database.QueryRowContext(ctx, `SELECT option_json,authority_body,authority_format FROM approval_operations WHERE checkpoint_id=?`, checkpointID).Scan(&optionJSON, &operationBody, &operationFormat))
	operationSum := sha256.Sum256([]byte(operationBody))
	for n := range records {
		nextID := fmt.Sprintf("recovery-%08d", n)
		_, err := host.database.ExecContext(ctx, `INSERT INTO checkpoints(id,session_id,project_id,kind,status,created_at) SELECT ?,session_id,project_id,kind,'pending',created_at FROM checkpoints WHERE id=?`, nextID, checkpointID)
		testutil.FailErr(t, "seed interrupted checkpoint", err)
		_, err = host.database.ExecContext(ctx, `INSERT INTO approval_operations(checkpoint_id,session_id,option_id,option_json,status,created_at,authority_format,authority_digest,authority_body) VALUES(?,'session',?,?,'prepared',?,?,?,?)`, nextID, optionID, optionJSON, createdAt, operationFormat, hex.EncodeToString(operationSum[:]), operationBody)
		testutil.FailErr(t, "seed interrupted operation", err)
	}
	recovery := productionAuthorityMemory(t)
	host.manager.Authority.SetNativeAuthorityAdmission(recovery)
	testutil.FailErr(t, "recover interrupted authority records", host.manager.Authority.RecoverApprovalOperations(ctx))
	if len(installer.rolledBack) != records || recovery.InUse().State != 0 || recovery.Peak().State > one.Peak().State+4096 {
		t.Fatalf("recovery retained batch authority: count=%d peak=%+v held=%+v", len(installer.rolledBack), recovery.Peak(), recovery.InUse())
	}
}
