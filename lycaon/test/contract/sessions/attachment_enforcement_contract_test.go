package contract

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// Prompt envelopes carry handles, not payloads or locators. Intake refusing a
// host path in place of a handle is internal/promptattach's
// TestIngestAttachmentsRefusesAHostPathHandle.
func TestPromptEnvelopeIntakeBoundaries(t *testing.T) {
	t.Parallel()
	for _, envelope := range []struct {
		typeOf    reflect.Type
		forbidden []string
	}{
		{reflect.TypeFor[api.PromptAttachmentPart](), []string{"url", "path", "file_path", "href", "bytes", "content"}},
		{reflect.TypeFor[api.PromptReferencePart](), []string{"url", "bytes", "content"}},
	} {
		fields := reflect.VisibleFields(envelope.typeOf)
		if len(fields) == 0 {
			t.Fatalf("%s has no fields to check", envelope.typeOf.Name())
		}
		for _, field := range fields {
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if field.PkgPath != "" || name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			for _, forbidden := range envelope.forbidden {
				if name == forbidden {
					t.Errorf("%s carries forbidden JSON field %q", envelope.typeOf.Name(), name)
				}
			}
			if field.Type.Kind() == reflect.Slice && field.Type.Elem().Kind() == reflect.Uint8 {
				t.Errorf("%s.%s carries inline bytes", envelope.typeOf.Name(), field.Name)
			}
		}
	}
}
