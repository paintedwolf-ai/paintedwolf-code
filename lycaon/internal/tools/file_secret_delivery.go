package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func (tc ToolContext) PrepareFileDelivery(ctx context.Context) (*secretcap.PreparedDelivery, error) {
	if !tc.Invocation.Contract.SecretReferenceSurface.IsFile() {
		return nil, nil
	}
	delivery, err := tc.Effects.Secrets.PrepareDelivery(ctx, nil)
	if err != nil {
		return nil, toolrejection.HeldHandOffReject(string(secretmatch.SurfaceFile), err)
	}
	return delivery, nil
}

func (tc ToolContext) WithholdFileDelivery(ctx context.Context) {
	if tc.Invocation.Contract.SecretReferenceSurface.IsFile() {
		tc.Effects.Secrets.Withhold(ctx)
	}
}
