package literal

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/tools"
	"testing"
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
	h := newHarness(t, map[string]string{".env": "VISIBLE=ok\nSECRET=person-secret-value\n"})
	h.tc.Effects.CredentialFiles = noAttributedInputDelivery{}
	h.exec("", "cat", ".env").expect(t, "credential input", "VISIBLE=ok\nSECRET=person-secret-value\n", "", 0)
}
