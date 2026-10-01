package contract

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// TestArtifactListReferenceOnlyContract locks the enumeration wire: metadata +
// origin refs only — never bytes/base64 and never host filesystem paths.
func TestArtifactListReferenceOnlyContract(t *testing.T) {
	t.Parallel()
	itemType := reflect.TypeOf(api.ArtifactListItem{})
	for i := 0; i < itemType.NumField(); i++ {
		name := itemType.Field(i).Name
		switch strings.ToLower(name) {
		case "bytes", "storeref", "path", "abspath", "filepath", "homedir":
			t.Fatalf("ArtifactListItem must not carry %s", name)
		}
		if itemType.Field(i).Type.Kind() == reflect.Slice && itemType.Field(i).Type.Elem().Kind() == reflect.Uint8 {
			t.Fatalf("ArtifactListItem must not carry []byte field %s", name)
		}
	}

	homeish := filepath.Join(string(filepath.Separator), "Users", "someone", "projects", "demo")
	resp := api.ArtifactListResponse{
		Artifacts: []api.ArtifactListItem{{
			ID:             "art-1",
			Mime:           "image/png",
			Source:         api.VisualArtifactSourceRender,
			Caption:        "mockup",
			EvidenceHandle: "page#1",
			SessionID:      "sess-1",
			WorkflowRunID:  "run-1",
			ToolCallID:     "tool-1",
			CreatedAt:      time.Date(2026, 7, 14, 15, 4, 5, 0, time.UTC),
		}},
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	if strings.Contains(body, `"bytes"`) || strings.Contains(body, "base64") {
		t.Fatal("ArtifactListResponse must not serialize bytes/base64")
	}
	if strings.Contains(body, homeish) || strings.Contains(body, "/Users/") || strings.Contains(body, `C:\`) {
		t.Fatal("ArtifactListResponse must not carry absolute host paths")
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal map: %v", err)
	}
	items, ok := decoded["artifacts"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("artifacts = %#v", decoded["artifacts"])
	}
	row, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("item = %#v", items[0])
	}
	for _, banned := range []string{"bytes", "store_ref", "path", "abspath"} {
		if _, present := row[banned]; present {
			t.Fatalf("list item must not include %q", banned)
		}
	}
}
