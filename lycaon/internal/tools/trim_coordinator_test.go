package tools

import "testing"

// TestTrimCoordinatorToolMetaKeepsGuidance preserves schema guidance and constraints.
func TestTrimCoordinatorToolMetaKeepsGuidance(t *testing.T) {
	longDesc := "Abort overlay (like git merge --abort). overlay_id == job_id. Wrong approach or superseded — not for landable conflicts. Example: {\"overlay_id\":\"<job_id>\",\"reason\":\"Superseded\"}."
	meta := ToolMeta{
		Name:        "reject_overlay",
		Description: longDesc,
		ArgsSchema: map[string]any{
			"type":     "object",
			"required": []any{"agent_type", "brief"},
			"properties": map[string]any{
				"brief": map[string]any{
					"type": "object",
				},
				"max_tool_loops": map[string]any{
					"type":        "integer",
					"minimum":     2,
					"maximum":     40,
					"description": "Optional cap on worker tool-loop iterations",
				},
				"agent_type": map[string]any{
					"type": "string",
					"enum": []any{"implementer", "repo-researcher"},
				},
				"files": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
			},
		},
	}

	out := TrimCoordinatorToolMeta(meta)
	if out.Description != longDesc {
		t.Fatalf("tool description was capped: got %q", out.Description)
	}

	props, ok := out.ArgsSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("trimmed schema dropped properties")
	}

	mtl, ok := props["max_tool_loops"].(map[string]any)
	if !ok {
		t.Fatal("trimmed schema dropped max_tool_loops property")
	}
	if mtl["minimum"] != 2 || mtl["maximum"] != 40 {
		t.Fatalf("max_tool_loops bounds = %v/%v want 2/40 — value constraints must survive", mtl["minimum"], mtl["maximum"])
	}
	if mtl["description"] != "Optional cap on worker tool-loop iterations" {
		t.Fatalf("property description was dropped: %v — param guidance must survive", mtl["description"])
	}

	if _, has := props["agent_type"].(map[string]any)["enum"]; !has {
		t.Fatal("enum must survive")
	}
	if _, has := props["files"].(map[string]any)["items"]; !has {
		t.Fatal("items must survive")
	}
}

func TestTrimCoordinatorToolMetaKeepsNestedArgumentContracts(t *testing.T) {
	t.Parallel()
	meta := ToolMeta{
		Name: "command",
		ArgsSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"env": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
				},
				"capability_request": map[string]any{
					"type":                 "object",
					"minProperties":        1,
					"additionalProperties": false,
					"properties": map[string]any{
						"socket_paths": map[string]any{
							"type":     "array",
							"minItems": 1,
							"maxItems": 8,
							"items":    map[string]any{"type": "string"},
						},
						"direct_ip": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"properties": map[string]any{
								"declared_destinations": map[string]any{
									"type":     "array",
									"maxItems": 16,
									"items": map[string]any{
										"type":      "string",
										"maxLength": 256,
									},
								},
							},
						},
					},
				},
			},
		},
	}

	out := TrimCoordinatorToolMeta(meta)
	props := schemaProperties(t, out.ArgsSchema)
	env := schemaMap(t, props["env"])
	envValues := schemaMap(t, env["additionalProperties"])
	if envValues["type"] != "string" {
		t.Fatalf("env additionalProperties=%v want string schema", envValues)
	}

	capability := schemaMap(t, props["capability_request"])
	if capability["minProperties"] != 1 || capability["additionalProperties"] != false {
		t.Fatalf("capability object constraints=%v", capability)
	}
	capabilityProps := schemaProperties(t, capability)
	sockets := schemaMap(t, capabilityProps["socket_paths"])
	if sockets["minItems"] != 1 || sockets["maxItems"] != 8 {
		t.Fatalf("socket path bounds=%v", sockets)
	}
	if schemaMap(t, sockets["items"])["type"] != "string" {
		t.Fatalf("socket path item schema=%v", sockets["items"])
	}
	directIP := schemaMap(t, capabilityProps["direct_ip"])
	destinations := schemaMap(t, schemaProperties(t, directIP)["declared_destinations"])
	if destinations["maxItems"] != 16 || schemaMap(t, destinations["items"])["maxLength"] != 256 {
		t.Fatalf("direct IP declaration constraints=%v", destinations)
	}
}

func TestTrimCoordinatorToolMetaKeepsDirectIPUnion(t *testing.T) {
	t.Parallel()
	meta := ToolMeta{
		Name: "command",
		ArgsSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"capability_request": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"direct_ip": map[string]any{
							"oneOf": []any{
								map[string]any{"type": "boolean"},
								map[string]any{"type": "object", "additionalProperties": false},
							},
						},
					},
				},
			},
		},
	}
	out := TrimCoordinatorToolMeta(meta)
	directIP := schemaMap(t, schemaProperties(t, schemaMap(t, schemaProperties(t, out.ArgsSchema)["capability_request"]))["direct_ip"])
	if _, hasType := directIP["type"]; hasType {
		t.Fatalf("union must not invent type: %v", directIP)
	}
	branches, ok := directIP["oneOf"].([]any)
	if !ok || len(branches) != 2 {
		t.Fatalf("oneOf = %v", directIP["oneOf"])
	}
	if schemaMap(t, branches[0])["type"] != "boolean" {
		t.Fatalf("first branch = %v want boolean", branches[0])
	}
}

func TestTrimCoordinatorToolMetaDropsEveryRootForbiddenKey(t *testing.T) {
	t.Parallel()
	for _, key := range FunctionParametersRootForbiddenKeys() {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			out := TrimCoordinatorToolMeta(ToolMeta{
				Name: "probe",
				ArgsSchema: map[string]any{
					"type": "object",
					key:    []any{map[string]any{"type": "string"}},
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
				},
			})
			if err := ValidateFunctionParametersRoot(out.ArgsSchema); err != nil {
				t.Fatalf("trimmed root: %v", err)
			}
			if _, has := out.ArgsSchema[key]; has {
				t.Fatalf("root %s survived trim", key)
			}
		})
	}
}

func TestTrimCoordinatorToolMetaDropsRootAnyOf(t *testing.T) {
	t.Parallel()
	meta := ToolMeta{
		Name: "code_rewrite",
		ArgsSchema: map[string]any{
			"type":     "object",
			"required": []any{"pattern", "rewrite"},
			"anyOf": []any{
				map[string]any{"required": []any{"path"}},
				map[string]any{"required": []any{"paths"}},
			},
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "minLength": 1},
				"paths":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"pattern": map[string]any{"type": "string"},
				"rewrite": map[string]any{"type": "string"},
			},
		},
	}
	out := TrimCoordinatorToolMeta(meta)
	if _, has := out.ArgsSchema["anyOf"]; has {
		t.Fatalf("root anyOf survived trim: %v", out.ArgsSchema["anyOf"])
	}
	if out.ArgsSchema["type"] != "object" {
		t.Fatalf("root type = %v want object", out.ArgsSchema["type"])
	}
	if _, has := schemaProperties(t, out.ArgsSchema)["path"]; !has {
		t.Fatal("path property dropped with root anyOf")
	}
}

func schemaProperties(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	return schemaMap(t, schema["properties"])
}

func schemaMap(t *testing.T, raw any) map[string]any {
	t.Helper()
	value, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("schema value=%T want map[string]any", raw)
	}
	return value
}
