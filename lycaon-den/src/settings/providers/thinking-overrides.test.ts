import { describe, expect, it } from "vitest";
import type { ModelPolicy, ThinkingOverride } from "../../api/types.ts";
import { replaceThinkingOverride, thinkingModelRefs, thinkingOptions, thinkingOverrideFor, thinkingOverrideValid } from "./thinking-overrides.ts";

const ref = { provider_id: "one", model: "shared" };
const fixed: ThinkingOverride = { ...ref, mode: "fixed", effort: "high" };
const policy: ModelPolicy = {
  coordinator: ref, lite: ref, agent_pool: { selection: "first", models: [ref] },
  thinking_overrides: [fixed, { ...fixed, provider_id: "two", effort: "low" }],
};

describe("thinking overrides", () => {
  it("keeps provider/model identity even when model names match", () => {
    expect(thinkingModelRefs(policy)).toHaveLength(2);
    const next = replaceThinkingOverride(policy, ref, undefined);
    expect(next).toEqual([{ ...fixed, provider_id: "two", effort: "low" }]);
    expect(policy.thinking_overrides).toHaveLength(2);
  });
  it("represents returning to application behavior separately from inheritance", () => {
    const project = { ...policy, thinking_overrides: [{ ...ref, mode: "application" as const }] };
    expect(thinkingOverrideFor(project, ref)?.mode).toBe("application");
    expect(replaceThinkingOverride(project, ref, undefined)).toEqual([]);
  });
  it("uses declared native levels and does not invent off or medium", () => {
    const capabilities = { state: "supported" as const, efforts: ["low", "high", "max"] };
    expect(thinkingOptions(capabilities, false).map((o) => o.value)).toEqual(["effort:low", "effort:high", "effort:max"]);
    expect(thinkingOverrideValid({ ...fixed, effort: "medium" }, capabilities)).toBe(false);
    expect(thinkingOverrideValid({ ...fixed, effort: "high" }, capabilities)).toBe(true);
    expect(thinkingOptions({ state: "unknown" }, false)).toEqual([]);
    expect(thinkingOptions({ state: "unknown" }, true)).toEqual([{ value: "application", label: "Use application behavior" }]);
  });
  it("validates a budget without inventing an upper bound", () => {
    const capabilities = { state: "supported" as const, budget: { min: 1024 } };
    expect(thinkingOverrideValid({ ...ref, mode: "fixed", budget_tokens: 4096 }, capabilities)).toBe(true);
    expect(thinkingOverrideValid({ ...ref, mode: "fixed", budget_tokens: 512 }, capabilities)).toBe(false);
  });
});
