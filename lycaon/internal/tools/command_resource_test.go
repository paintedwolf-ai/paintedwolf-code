package tools

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestResourceRejectionPreservesActualAdmissionAndExistingCause(t *testing.T) {
	for account, id := range map[string]string{"working": "evaluation_memory_bytes", "state": "program_state_bytes", "session_spill_bytes": "session_spill_bytes"} {
		original := &toolcontract.ProgramMemoryLimitError{Account: account, Maximum: 123, Observed: 456}
		err := CommandResourceRejection(original)
		reject := toolrejection.AsToolReject(err)
		if !errors.Is(err, original) || reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" || reject.ArgumentValidation || reject.Data["limit_id"] != id || reject.Data["resource_unit"] != "bytes" || reject.Data["maximum"] != int64(123) || reject.Data["observed"] != int64(456) {
			t.Fatalf("account %s cause %v reject %+v", account, err, reject)
		}
		existing := &toolrejection.ToolReject{Code: "EXACT_OWNER_REFUSAL"}
		wrapped := errors.Join(existing, original)
		if !errors.Is(CommandResourceRejection(wrapped), wrapped) {
			t.Fatal("existing refusal replaced")
		}
	}
	unknown := &toolcontract.ProgramMemoryLimitError{Account: "unknown", Maximum: 1, Observed: 2}
	if !errors.Is(CommandResourceRejection(unknown), unknown) {
		t.Fatal("invented a catalog limit for unknown account")
	}
}

func TestResourceRejectionPreservesActualProgramLimit(t *testing.T) {
	original := &commandprogram.ProgramLimitError{Limit: "function_depth", Maximum: 32, Observed: 33}
	err := CommandResourceRejection(original)
	reject := toolrejection.AsToolReject(err)
	if !errors.Is(err, original) || reject == nil || reject.ArgumentValidation || reject.Code != "COMMAND_LIMIT_EXCEEDED" || reject.Data["limit_id"] != original.Limit || reject.Data["maximum"] != original.Maximum || reject.Data["observed"] != original.Observed || reject.Data["resource_unit"] != "frames" {
		t.Fatalf("program limit cause %v reject %+v", err, reject)
	}
}
