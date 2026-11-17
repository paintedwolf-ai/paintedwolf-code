package structure

import (
	"strings"
	"testing"
)

func TestDeclaredBoundary(t *testing.T) {
	t.Parallel()
	graph, err := BuildGraph(fakeTree(map[string]string{
		"lycaon/internal/example/example.go":           "package example\nimport \"github.com/lycaon/lycaon/internal/example/private/deep\"",
		"lycaon/internal/example/private/deep/deep.go": "package deep",
		"lycaon/internal/app/app.go":                   "package app\nimport \"github.com/lycaon/lycaon/internal/example/private/deep\"",
	}))
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	cases := []struct {
		name        string
		declaration Declaration
		want        string
	}{
		{"private import", Declaration{Kind: subsystem, Description: "Example", EntryPoints: []string{module + "/internal/example"}}, "internal/app imports private package internal/example/private/deep"},
		{"missing entry", Declaration{Kind: subsystem, Description: "Example", EntryPoints: []string{module + "/internal/example/missing"}}, "existing production package"},
		{"unrelated entry", Declaration{Kind: subsystem, Description: "Example", EntryPoints: []string{module + "/internal/app"}}, "within the declared directory"},
		{"missing contract", Declaration{Kind: family, Contract: module + "/internal/example/missing"}, "existing production package"},
		{"unrelated contract", Declaration{Kind: family, Contract: module + "/internal/app"}, "within the declared directory"},
		{"explicit nested entry", Declaration{Kind: subsystem, Description: "Example", EntryPoints: []string{module + "/internal/example", module + "/internal/example/private/deep"}}, ""},
		{"family composition", Declaration{Kind: family, Contract: module + "/internal/example"}, ""},
		{"container", Declaration{Kind: container}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Analyze(graph, "internal/example", tc.declaration)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("analyze: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("analyze error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSubsystemInternalCollaboratorsRemainAdvisory(t *testing.T) {
	t.Parallel()
	graph := graphFor(map[string][]string{
		"internal/example":             {"internal/app"},
		"internal/example/private":     {"internal/example"},
		"internal/example/unconnected": nil,
	}, nil)
	d, err := Analyze(graph, "internal/example", Declaration{Kind: subsystem, Description: "Example", EntryPoints: []string{module + "/internal/example"}})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if len(d.Groups) < 2 || len(d.Isolated) == 0 {
		t.Fatalf("expected advisory evidence: %#v", d)
	}
}
