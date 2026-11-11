package query

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	nativejq "github.com/lycaon/lycaon/internal/tools/native/jq"
)

func (in *inputs) parseFailure(err error) {
	var limit *nativejq.DecodeLimitError
	if errors.As(err, &limit) {
		in.stop = tools.CommandLimitExceeded(limit.Limit, limit.Maximum, limit.Observed)
		return
	}
	cause := tools.CommandResourceRejection(err)
	if toolrejection.AsToolReject(cause) != nil || errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		in.stop = cause
		return
	}
	in.malformed(err)
}
