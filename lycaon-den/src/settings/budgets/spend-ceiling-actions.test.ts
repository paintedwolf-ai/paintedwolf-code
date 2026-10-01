import { describe, expect, it, vi } from "vitest";
import type { SettingsLimitsResponse } from "../../api/types.ts";
import { NANO_PER_USD } from "../../cost/nano-usd.ts";
import { raiseEffectiveSpendCeiling } from "./spend-ceiling-actions.ts";

function limits(ceiling: number): SettingsLimitsResponse {
  return {
    scope: "global", max_iterations: 500, overlay_promote_max_iterations: 150,
    max_tool_result_bytes: 0, coordinator_loop: true, max_coordinator_loop_cycles: 0,
    llm_turn_timeout_ms: 60000, coordinator_host_turn_timeout_ms: 60000,
    coordinator_max_sleep_ms: 60000, await_parent_workers_timeout_ms: 60000,
    worker_tool_budget_default: 20,
    worker_tool_budget_min: 2, worker_tool_budget_max: 120,
    session_spend_ceiling_nano_usd: ceiling * NANO_PER_USD, spend_warning_ratio: 0.8,
    spend_ceiling_enabled: true, spend_soft_stop: true, merged_from: ["bundled"],
  };
}

describe("raiseEffectiveSpendCeiling", () => {
  it.each([
    { name: "a lower project ceiling", global: 10, project: 5, spent: 5, scopes: ["project"] },
    { name: "both ceilings exceeded", global: 5, project: 2, spent: 6, scopes: ["global", "project"] },
    { name: "an explicit project ceiling equal to the device ceiling", global: 5, project: 5, spent: 6, scopes: ["global", "project"] },
    { name: "an inherited device ceiling", global: 5, project: null, spent: 6, scopes: ["global"] },
  ])("leaves room to continue with $name", async (scenario) => {
    let global = limits(scenario.global);
    let projectCeiling = scenario.project;
    const effective = () => ({
      ...global,
      scope: "project" as const,
      session_spend_ceiling_nano_usd: Math.min(global.session_spend_ceiling_nano_usd ?? 0, projectCeiling != null ? projectCeiling * NANO_PER_USD : Infinity),
    });
    const getLimitsSettings = vi.fn(async (projectId?: string) => projectId ? effective() : global);
    const updateLimitsSettings = vi.fn(async (body: { session_spend_ceiling_nano_usd: number }, projectId?: string) => {
      if (!projectId) global = { ...global, ...body };
      else projectCeiling = body.session_spend_ceiling_nano_usd < (global.session_spend_ceiling_nano_usd ?? 0)
        ? body.session_spend_ceiling_nano_usd / NANO_PER_USD : null;
      return projectId ? effective() : global;
    });

    await raiseEffectiveSpendCeiling({ getLimitsSettings, updateLimitsSettings } as never, "project-1", scenario.spent);

    expect((effective().session_spend_ceiling_nano_usd ?? 0) / NANO_PER_USD).toBeGreaterThan(scenario.spent);
    for (const [body] of updateLimitsSettings.mock.calls) {
      expect(Object.keys(body).sort()).toEqual(["session_spend_ceiling_nano_usd", "spend_ceiling_enabled"]);
    }
    expect(updateLimitsSettings.mock.calls.map(([, projectId]) => projectId ? "project" : "global")).toEqual(scenario.scopes);
  });

  it("raises a lower project guardrail without weakening the device guardrail", async () => {
    const global = limits(10);
    const project = { ...limits(5), scope: "project" as const, merged_from: ["bundled", "global", "project"] };
    const getLimitsSettings = vi.fn()
      .mockResolvedValueOnce(global)
      .mockResolvedValueOnce(project);
    const updateLimitsSettings = vi.fn().mockResolvedValue({ ...project, session_spend_ceiling_nano_usd: 10 * NANO_PER_USD });

    const update = await raiseEffectiveSpendCeiling(
      { getLimitsSettings, updateLimitsSettings } as never,
      "project-1",
      4.5,
    );

    expect(update.scope).toBe("project");
    expect(updateLimitsSettings).toHaveBeenCalledWith(
      { spend_ceiling_enabled: true, session_spend_ceiling_nano_usd: 10 * NANO_PER_USD },
      "project-1",
    );
  });
});
