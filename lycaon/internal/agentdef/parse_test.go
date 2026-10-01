package agentdef

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseSkills(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		yaml      string
		wantAll   bool
		wantNames []string
		wantErr   string
	}{
		{name: "all", yaml: "skills: all\n", wantAll: true},
		{name: "unknown sibling key", yaml: "skills: all\nskills_pinned: [verify-a-change]\n", wantErr: "skills_pinned"},
		{name: "exact", yaml: "skills: [investigate-code-history, verify-a-change]\n", wantNames: []string{"investigate-code-history", "verify-a-change"}},
		{name: "omitted", yaml: "", wantNames: nil},
		{name: "empty", yaml: "skills: []\n", wantErr: "omit the field"},
		{name: "invalid scalar", yaml: "skills: recommended\n", wantErr: "scalar must be"},
		{name: "invalid name", yaml: "skills: [Bad_Name]\n", wantErr: "invalid skill name"},
		{name: "duplicate", yaml: "skills: [verify-a-change, verify-a-change]\n", wantErr: "duplicate skill name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			profile, err := Parse([]byte("id: worker\ntool_profile: implement\n" + tt.yaml))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if profile.Skills.All != tt.wantAll || !reflect.DeepEqual(profile.Skills.Names, tt.wantNames) {
				t.Fatalf("skills = %#v, want all=%v names=%#v", profile.Skills, tt.wantAll, tt.wantNames)
			}
		})
	}
}

// The tool profile is the only grant, so a tools: key is refused as unknown.
func TestParseRejectsToolsKey(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte("id: fixture\ntool_profile: reader\ntools:\n  - read\n"))
	if err == nil {
		t.Fatal("Parse accepted a tools: key; the tool profile is the only grant")
	}
	if !strings.Contains(err.Error(), "tools") {
		t.Fatalf("error = %v, want it to name the unknown field", err)
	}
}
