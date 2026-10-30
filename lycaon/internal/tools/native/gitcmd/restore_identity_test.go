package gitcmd

import (
	"errors"
	"io"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestRestorePreconditionUsesTypedIdentity(t *testing.T) {
	result, err := settleRestoreError(&git.OperationError{Reason: "repository_changed_during_review", Paths: []string{"file.txt"}}, io.Discard)
	reject := toolrejection.AsToolReject(err)
	if result.Terminal != toolcontract.ProducerRefused || reject == nil || reject.Code != "GIT_OPERATION_PRECONDITION" || reject.Data["reason"] != "repository_changed_during_review" {
		t.Fatalf("typed restore refusal=%+v err=%v", result, err)
	}
	result, err = settleRestoreError(errors.New("file changed during restore review"), io.Discard)
	if err != nil || result.Terminal == toolcontract.ProducerRefused || result.Status != 128 {
		t.Fatalf("diagnostic prose acquired precondition authority: %+v err=%v", result, err)
	}
}
