package native

import (
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
)

func TestSecretGenerationRejectsLossyArgumentsBeforeStoreAccess(t *testing.T) {
	for _, bad := range []struct {
		field string
		value any
	}{
		{"bytes", 16.5}, {"bytes", 0}, {"bytes", "32"}, {"bytes", nil},
		{"agent_use_ttl_seconds", 60.5}, {"agent_use_ttl_seconds", 0}, {"agent_use_ttl_seconds", float64(1 << 63)},
		{"scope", true}, {"scope", ""}, {"format", 42}, {"purpose", nil},
	} {
		args := map[string]any{"name": "fixture", bad.field: bad.value}
		_, err := generateSecret(t.Context(), nil, args, tools.ToolContext{})
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "SECRET_GENERATE_INVALID" || reject.Data["field"] != bad.field {
			t.Fatalf("lossy input reached generation: field=%s err=%v", bad.field, err)
		}
	}
}
