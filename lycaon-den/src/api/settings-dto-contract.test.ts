import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { openAPISchemaProperties, readOpenAPIBundle } from "./openapi-sync.ts";

const typesPath = join(dirname(fileURLToPath(import.meta.url)), "types.ts");

describe("settings DTO OpenAPI sync", () => {
  it("ModelPolicy and ApprovalConfigResponse fields exist in OpenAPI and TS", () => {
    const yaml = readOpenAPIBundle();
    const ts = readFileSync(typesPath, "utf8");

    for (const field of ["coordinator", "lite", "agent_pool"]) {
      expect(openAPISchemaProperties(yaml, "ModelPolicy")).toContain(field);
      expect(openAPISchemaProperties(yaml, "ModelPolicyPatch")).toContain(field);
      expect(ts).toMatch(new RegExp(`${field}:`));
    }

    for (const field of [
      "scope",
      "rules",
      "managed_rules",
      "merged_from",
      "approval_posture",
      "field_sources",
      "defaults",
    ]) {
      expect(openAPISchemaProperties(yaml, "ApprovalConfigResponse")).toContain(
        field,
      );
      expect(ts).toMatch(new RegExp(`${field}\\??:`));
    }
  });

  it("ProviderMeta schema omits api_key from GET shape", () => {
    const yaml = readOpenAPIBundle();
    const props = openAPISchemaProperties(yaml, "ProviderMeta");
    expect(props).not.toContain("api_key");
    expect(props).toContain("configured");
    expect(props).toContain("models");
  });

  it("McpProvider and McpCheckRow fields match OpenAPI and TS", () => {
    const yaml = readOpenAPIBundle();
    const ts = readFileSync(typesPath, "utf8");

    for (const field of ["id", "enabled"]) {
      expect(openAPISchemaProperties(yaml, "McpProvider")).toContain(field);
      expect(ts).toMatch(new RegExp(`${field}:`));
    }
    for (const field of [
      "profiles",
      "last_error",
      "source",
      "connection_source",
      "transport",
      "status",
      "tool_count",
      "token_present",
      "headers_present",
      "env_present",
      "signed_in",
      "notice",
      "credential_wire",
      "credential_header",
    ]) {
      expect(openAPISchemaProperties(yaml, "McpProvider")).toContain(field);
      expect(ts).toMatch(new RegExp(`${field}\\?`));
    }

    for (const field of ["provider_id", "status", "code", "notice"]) {
      expect(openAPISchemaProperties(yaml, "McpCheckRow")).toContain(field);
    }
    expect(ts).toMatch(/status:/);
  });
});
