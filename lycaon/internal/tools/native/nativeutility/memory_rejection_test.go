package nativeutility

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestResourceRejectionPreservesActualAdmissionAndExistingCause(t *testing.T) {
	for account, id := range map[string]string{"working": "evaluation_memory_bytes", "state": "program_state_bytes", "session_spill_bytes": "session_spill_bytes"} {
		original := &toolcontract.ProgramMemoryLimitError{Account: account, Maximum: 123, Observed: 456}
		err := ResourceRejection(original)
		reject := toolrejection.AsToolReject(err)
		if !errors.Is(err, original) || reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" || reject.Data["limit_id"] != id || reject.Data["maximum"] != int64(123) || reject.Data["observed"] != int64(456) {
			t.Fatalf("account %s cause %v reject %+v", account, err, reject)
		}
		result := Settle(err, 0, false)
		if result.Terminal != toolcontract.ProducerRefused || result.StopCode != reject.Code || result.PublicationEligible {
			t.Fatalf("admission became utility success %+v", result)
		}
		existing := &toolrejection.ToolReject{Code: "EXACT_OWNER_REFUSAL"}
		wrapped := errors.Join(existing, original)
		if ResourceRejection(wrapped) != wrapped {
			t.Fatal("existing refusal replaced")
		}
	}
	unknown := &toolcontract.ProgramMemoryLimitError{Account: "unknown", Maximum: 1, Observed: 2}
	if ResourceRejection(unknown) != unknown {
		t.Fatal("invented a catalog limit for unknown account")
	}
}
