package query

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
	h := newHarness(t, map[string]string{"values.json": `{"visible":"ok","secret":"person-secret-value"}`})
	h.tc.Effects.CredentialFiles = noAttributedInputDelivery{}
	h.exec("", "jq", "-r", ".visible", "values.json").expect(t, "filtered credential input", "ok\n", 0)
}
