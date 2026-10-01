import { describe, expect, it } from "vitest";
import {
  allCheckpointStatuses,
  allWorkerStatuses,
  allWorkerSummaryStatuses,
  allWorkflowBoundaryKinds,
  allWorkflowRunStatuses,
  resolvedCheckpointStatuses,
  WIRE_ENUM_REGISTRY_NAMES,
  wireEnumValues,
} from "./enum-registries.ts";
import {
  parseOpenAPIEnums,
  parseTSEnumUnions,
  readOpenAPIBundle,
  sortedSetEqual,
} from "./openapi-sync.ts";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const typesPath = join(dirname(fileURLToPath(import.meta.url)), "types.ts");

describe("wire enum registries", () => {
  it("matches generated types.ts and OpenAPI for every registered schema", () => {
    const tsEnums = parseTSEnumUnions(readFileSync(typesPath, "utf8"));
    const oapiEnums = parseOpenAPIEnums(readOpenAPIBundle());
    for (const name of WIRE_ENUM_REGISTRY_NAMES) {
      const registry = [...wireEnumValues(name)].sort();
      const fromTs = [...(tsEnums.get(name) ?? [])].sort();
      const fromOapi = [...(oapiEnums.get(name) ?? [])].sort();
      expect(registry.length, name).toBeGreaterThan(0);
      expect(sortedSetEqual(registry, fromTs), `${name} vs types.ts`).toBe(
        true,
      );
      expect(sortedSetEqual(registry, fromOapi), `${name} vs OpenAPI`).toBe(
        true,
      );
    }
  });

  it("resolvedCheckpointStatuses is every status except pending", () => {
    const all = allCheckpointStatuses();
    const resolved = resolvedCheckpointStatuses();
    expect(resolved).not.toContain("pending");
    expect(resolved).toHaveLength(all.length - 1);
    expect(new Set(resolved).size).toBe(resolved.length);
    for (const status of all) {
      if (status === "pending") continue;
      expect(resolved).toContain(status);
    }
  });

  it("exports non-empty registries for invariant consumers", () => {
    expect(allWorkerStatuses().length).toBeGreaterThan(0);
    expect(allWorkerSummaryStatuses().length).toBeGreaterThan(0);
    expect(allWorkflowRunStatuses().length).toBeGreaterThan(0);
    expect(allWorkflowBoundaryKinds().length).toBeGreaterThan(0);
  });
});
