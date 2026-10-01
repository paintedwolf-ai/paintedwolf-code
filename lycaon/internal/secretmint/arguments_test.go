package secretmint

import "testing"

func TestCredentialSlotsAcrossInvocationShapes(t *testing.T) {
	ins := testInspector(t)
	cases := []struct {
		name, tool string
		args       map[string]any
		assignment string
	}{
		{"nested executable", "command", map[string]any{"command": "docker exec service gitea admin user create --username admin --password password"}, "--password"},
		{"wrapper delimiter", "command", map[string]any{"command": "wrapper -- child --client-secret=password"}, "--client-secret"},
		{"nested environment", "command", map[string]any{"command": "docker run -e APP_PASSWORD=password image"}, "APP_PASSWORD"},
		{"structured unknown tool", "external_call", map[string]any{"payload": map[string]any{"clientSecret": "password"}}, "clientSecret"},
		{"structured list", "external_call", map[string]any{"payload": []any{map[string]any{"password": "password"}}}, "password"},
		{"terminal", "terminal_send", map[string]any{"input": "service --password=password\n"}, "--password"},
		{"long flag credential use", "command", map[string]any{"command": "docker login --password=password example.test"}, "--password"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits := ins.Inspect(tc.tool, tc.args)
			if !hasAssignment(hits, tc.assignment) {
				t.Fatalf("missing %s: %v", tc.assignment, assignments(hits))
			}
		})
	}
}

func TestCredentialSlotsDoNotGuessFromOrdinaryValues(t *testing.T) {
	ins := testInspector(t)
	for _, args := range []map[string]any{
		{"path": "password", "username": "admin", "port": "1234"},
		{"next_token": "password", "pageToken": "password", "password_file": "password"},
		{"command": "example --page-token password --password-file password"},
		{"password": "{{paintedwolf-secret:123e4567-e89b-42d3-a456-426614174000}}"},
		{"command": "example --password $PASSWORD"},
	} {
		if hits := ins.Inspect("command", args); len(hits) != 0 {
			t.Fatalf("unexpected slots: %v", assignments(hits))
		}
	}
}

func TestCredentialCatalogRejectsUnknownParser(t *testing.T) {
	cat := Catalog{Surfaces: map[string]Surface{"example": {Kind: "unknown"}}}
	if cat.validate() == nil {
		t.Fatal("unknown parser accepted")
	}
}
