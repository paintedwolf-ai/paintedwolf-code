package contract

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// TestVisualArtifactPlanePurityContract keeps the Visual plane distinct from
// coordination digests: VisualArtifact must not carry workbook/findings/progress
// fields — plane purity is a type boundary.
func TestVisualArtifactPlanePurityContract(t *testing.T) {
	t.Parallel()
	visualType := reflect.TypeOf(api.VisualArtifact{})
	forbidden := map[string]string{
		"Findings": "FindingsDigest",
		"Finding":  "Finding",
		"Steps":    "ProgressDigest",
		"Progress": "ProgressDigest",
		"Workbook": "session workbook",
		"Revision": "coordination digest revision",
		"ActiveMs": "ProgressDigest",
		"Running":  "ProgressDigest",
	}
	for i := 0; i < visualType.NumField(); i++ {
		field := visualType.Field(i)
		if plane, bad := forbidden[field.Name]; bad {
			t.Fatalf("VisualArtifact must not carry coordination field %s (%s plane)", field.Name, plane)
		}
	}

	// Findings / progress must not grow Visual fields either.
	for _, pair := range []struct {
		name string
		typ  reflect.Type
	}{
		{"FindingsDigest", reflect.TypeOf(api.FindingsDigest{})},
		{"Finding", reflect.TypeOf(api.Finding{})},
		{"ProgressDigest", reflect.TypeOf(api.ProgressDigest{})},
		{"ProgressStep", reflect.TypeOf(api.ProgressStep{})},
	} {
		for i := 0; i < pair.typ.NumField(); i++ {
			field := pair.typ.Field(i)
			switch field.Name {
			case "Visual", "VisualArtifact", "Mime", "Bytes", "Perceive", "StoreRef":
				t.Fatalf("%s must not carry Visual plane field %s", pair.name, field.Name)
			}
		}
	}
}

// TestVisualArtifactAllowedFieldsLocked documents the VisualArtifact wire shape.
func TestVisualArtifactAllowedFieldsLocked(t *testing.T) {
	t.Parallel()
	want := map[string]struct{}{
		"ID": {}, "Mime": {}, "Bytes": {}, "StoreRef": {}, "Source": {},
		"Caption": {}, "EvidenceHandle": {}, "PageID": {}, "RecordedAt": {}, "DurationMS": {},
		"Width": {}, "Height": {}, "Perceive": {}, "ToolCallID": {}, "OriginMessageID": {},
	}
	visualType := reflect.TypeOf(api.VisualArtifact{})
	if visualType.NumField() != len(want) {
		t.Fatalf("VisualArtifact field count = %d want %d — update lock when extending Visual plane only", visualType.NumField(), len(want))
	}
	for i := 0; i < visualType.NumField(); i++ {
		name := visualType.Field(i).Name
		if _, ok := want[name]; !ok {
			t.Fatalf("unexpected VisualArtifact field %s — keep plane pure of coordination data", name)
		}
	}
}
