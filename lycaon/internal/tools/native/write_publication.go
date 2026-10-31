package native

import (
	"sync"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// UnpublishedWrite retains an accepted editor effect when its disk save fails.
// The command stops here; the held revision remains the recovery authority.
type UnpublishedWrite struct {
	Result     WriteResult
	rejectOnce sync.Once
	reject     *toolrejection.ToolReject
}

func (e *UnpublishedWrite) Error() string { return "COMMAND_PUBLICATION_PENDING" }
func (e *UnpublishedWrite) Unwrap() []error {
	e.rejectOnce.Do(func() {
		e.reject = &toolrejection.ToolReject{Code: "COMMAND_PUBLICATION_PENDING", Data: map[string]any{"path": e.Result.Path, "document_id": e.Result.DocumentID, "held_version_id": e.Result.HeldVersionID}}
	})
	causes := []error{e.reject}
	if e.Result.PublicationError != nil {
		causes = append(causes, e.Result.PublicationError)
	}
	return causes
}
func (e *UnpublishedWrite) ProducerResult() toolcontract.ProducerResult {
	accepted, _ := e.Result.SettlementRevisions()
	return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: "COMMAND_PUBLICATION_PENDING", EffectCertainty: toolcontract.EffectCompleted, Evidence: toolcontract.OperationEvidence{OwnerRef: e.Result.DocumentID, Kind: toolcontract.AcceptedUnpublishedWrite, EvidenceRef: accepted, RecoveryRef: e.Result.HeldVersionID, HeldVersionID: e.Result.HeldVersionID}}
}
