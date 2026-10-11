package native

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// Structured reject codes for recall.
const (
	recallQueryInvalid       = "RECALL_QUERY_INVALID"
	recallScopeNotPermitted  = "RECALL_SCOPE_NOT_PERMITTED"
	recallLiveCodeNotInScope = "RECALL_LIVE_CODE_NOT_IN_SCOPE"
	recallUnavailable        = "RECALL_UNAVAILABLE"
)

func runRecall(ctx context.Context, args map[string]any, tctx tools.ToolContext, svc *recall.Service) (string, error) {
	query, _ := args["query"].(string)
	if strings.TrimSpace(query) == "" {
		return "", &toolrejection.ToolReject{
			Code:      recallQueryInvalid,
			Retryable: true,
			Data:      map[string]any{"reason": "empty query", "recall_allowed_fields": search.DSLFieldAllowlist()},
		}
	}
	widen, err := recall.ParseWiden(stringArgOrEmpty(args, "widen"))
	if err != nil {
		return "", &toolrejection.ToolReject{
			Code:      recallScopeNotPermitted,
			Retryable: true,
			Data:      map[string]any{"reason": "unknown widen value", "recall_widen_invalid": true},
		}
	}

	result, err := svc.Answer(ctx, recall.Request{
		Query: query,
		Widen: widen,
		Limit: intArgOrZero(args, "limit"),
		Caller: recall.Caller{
			SessionID:       tctx.Identity.SessionID,
			ParentSessionID: tctx.Identity.ParentSessionID,
			ProjectID:       tctx.Identity.ProjectID,
		},
	})
	if err != nil {
		return "", recallReject(err)
	}

	for _, hit := range result.Hits {
		tctx.RecordSourceContext(hit.SourceContext)
	}
	raw, err := surveyjson.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("encode recall result: %w", err)
	}
	return string(raw), nil
}

// recallReject gives every reachable failure its own code so a caller can
// branch on it.
func recallReject(err error) error {
	var denied *recall.ErrScopeDenied
	if asScopeDenied(err, &denied) {
		return &toolrejection.ToolReject{
			Code:      recallScopeNotPermitted,
			Retryable: true,
			Data:      map[string]any{"role": string(denied.Role), "widen": string(denied.Widen)},
		}
	}
	var liveCode *recall.ErrLiveCodeRequested
	if asLiveCode(err, &liveCode) {
		return &toolrejection.ToolReject{
			Code:      recallLiveCodeNotInScope,
			Retryable: true,
			Data:      map[string]any{"kind": liveCode.Kind},
		}
	}
	var parseErr *search.ParseError
	if asParseError(err, &parseErr) {
		return &toolrejection.ToolReject{
			Code:      recallQueryInvalid,
			Retryable: true,
			Data: map[string]any{
				"field":                 parseErr.Field,
				"recall_allowed_fields": search.DSLFieldAllowlist(),
				"offset":                parseErr.Offset,
				"reason":                parseErr.Message,
			},
		}
	}
	return &toolrejection.ToolReject{
		Code:      recallUnavailable,
		Retryable: false,
		Data:      map[string]any{"reason": err.Error()},
	}
}

func asScopeDenied(err error, target **recall.ErrScopeDenied) bool {
	var v *recall.ErrScopeDenied
	ok := errors.As(err, &v)
	if ok {
		*target = v
	}
	return ok
}

func asLiveCode(err error, target **recall.ErrLiveCodeRequested) bool {
	var v *recall.ErrLiveCodeRequested
	ok := errors.As(err, &v)
	if ok {
		*target = v
	}
	return ok
}

func asParseError(err error, target **search.ParseError) bool {
	var v *search.ParseError
	ok := errors.As(err, &v)
	if ok {
		*target = v
	}
	return ok
}

func stringArgOrEmpty(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

func intArgOrZero(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}

func RegisterRecallTool(reg *tools.DefaultRegistry, svc *recall.Service) error {
	if reg == nil {
		return fmt.Errorf("registry required")
	}
	if svc == nil {
		return fmt.Errorf("recall service required")
	}
	return reg.Register(recall.ToolName, func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		return runRecall(ctx, args, tctx, svc)
	})
}
