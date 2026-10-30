//go:build darwin || linux

package textutil

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
)

type noAttributedInputDelivery struct{}

func (noAttributedInputDelivery) EnsureProject(context.Context, string) error { return nil }
func (noAttributedInputDelivery) ForgetProject(context.Context, string) error { return nil }
func (noAttributedInputDelivery) Authored(context.Context, tools.AuthoredCredentialValues) error {
	return nil
}
func (noAttributedInputDelivery) Delivered(context.Context, tools.CredentialFileRead) error {
	return errors.New("captured command input is not an attributed delivery")
}

func TestCapturingInputDoesNotReportAttributedDelivery(t *testing.T) {
	h := newHarness(t)
	h.seed(".env", "VISIBLE=ok\nSECRET=person-secret-value\n")
	h.tc.Effects.CredentialFiles = noAttributedInputDelivery{}
	result := h.run("sed -n 1p .env", "")
	if result.err != nil || result.stdout != "VISIBLE=ok\n" || result.result.Status != 0 {
		t.Fatalf("captured input became attributed delivery: output=%q result=%+v error=%v", result.stdout, result.result, result.err)
	}
}
