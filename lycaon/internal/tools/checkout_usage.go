package tools

import "context"

// CheckoutUsageRelation distinguishes checkout creation from later activity inside a checkout.
type CheckoutUsageRelation string

const (
	CheckoutUsageCreated CheckoutUsageRelation = "created"
	CheckoutUsageUsed    CheckoutUsageRelation = "used"
)

// CheckoutUsageRecorder associates authorized tool and command execution with registered checkouts.
type CheckoutUsageRecorder interface {
	RecordCheckoutActivity(ctx context.Context, tc ToolContext, targetPath string, activityID string, relation CheckoutUsageRelation) string
	NotifyCheckoutChange(ctx context.Context, tc ToolContext, repoPath string)
}

// RecordCheckoutCreated records that an authorized command created a registered checkout.
func (c ToolContext) RecordCheckoutCreated(ctx context.Context, targetPath, activityID string) string {
	if c.Source.CheckoutUsage == nil {
		return ""
	}
	return c.Source.CheckoutUsage.RecordCheckoutActivity(ctx, c, targetPath, activityID, CheckoutUsageCreated)
}

// RecordCheckoutUsed records that an authorized command or file effect operated inside a registered checkout.
func (c ToolContext) RecordCheckoutUsed(ctx context.Context, targetPath, activityID string) string {
	if c.Source.CheckoutUsage == nil {
		return ""
	}
	return c.Source.CheckoutUsage.RecordCheckoutActivity(ctx, c, targetPath, activityID, CheckoutUsageUsed)
}

// NotifyCheckoutChange schedules asynchronous repository checkout reconciliation after a registration change.
func (c ToolContext) NotifyCheckoutChange(ctx context.Context, repoPath string) {
	if c.Source.CheckoutUsage == nil {
		return
	}
	c.Source.CheckoutUsage.NotifyCheckoutChange(ctx, c, repoPath)
}
