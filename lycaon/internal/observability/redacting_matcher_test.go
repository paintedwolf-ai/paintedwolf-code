package observability

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLogPersistenceScreensCurrentEvidence(t *testing.T) {
	const catalog = "ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3"
	const managed = "private-managed-canary-4411"
	const harvested = "unshaped-harvest-canary-5533"
	text := strings.Join([]string{catalog, managed, harvested}, " ")
	var output bytes.Buffer
	logger := slog.New(NewRedactingHandler(slog.NewJSONHandler(&output, nil))).With("bound", text).WithGroup("request")
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build matcher", err)
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{
			{Secret: managed, Name: "managed", RuleID: secretmatch.ManagedRuleID, NonDisclosable: true},
			{Secret: harvested, Name: "from file", RuleID: secretmatch.HarvestRuleID},
		}
	})
	release := SetCaptureRedactor(func(s string) string { return matcher.RedactString(context.Background(), s) })
	t.Cleanup(release)
	logger.LogAttrs(t.Context(), slog.LevelInfo, text,
		slog.Group("nested", slog.String("value", text)), slog.Any("error", fmt.Errorf("upstream: %s", text)))
	for _, value := range []string{catalog, managed, harvested} {
		if strings.Contains(output.String(), value) {
			t.Fatalf("log retained secret: %s", output.String())
		}
	}
	if !strings.Contains(output.String(), "bound") || !strings.Contains(output.String(), "nested") || !strings.Contains(output.String(), "[REDACTED]") {
		t.Fatalf("screened log = %s", output.String())
	}
}

func TestLogMatcherFailureDropsContent(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(NewRedactingHandler(slog.NewJSONHandler(&output, nil))).With("bound", "private-bound")
	release := SetCaptureRedactor(func(string) string { panic("private-error") })
	t.Cleanup(release)
	logger.Info("private-message", "value", "private-attribute")
	if strings.Contains(output.String(), "private-") || strings.Contains(output.String(), "bound") || !strings.Contains(output.String(), "screening failed") {
		t.Fatalf("unsafe failure record = %s", output.String())
	}
}
