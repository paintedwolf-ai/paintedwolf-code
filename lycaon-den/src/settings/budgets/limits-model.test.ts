// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { createRoot, createSignal } from "solid-js";
import { changedLimitsFields, createLimitsDraft } from "./limits-model.ts";
import type { SettingsLimitsResponse } from "../../api/types.ts";

const sampleLimits = (): SettingsLimitsResponse => ({
  scope: "global",
  max_iterations: 500,
  overlay_promote_max_iterations: 150,
  max_tool_result_bytes: 131072,
  coordinator_loop: true,
  max_coordinator_loop_cycles: 64,
  llm_turn_timeout_ms: 10800000,
  coordinator_host_turn_timeout_ms: 10800000,
  coordinator_max_sleep_ms: 10800000,
  await_parent_workers_timeout_ms: 10800000,
  worker_tool_budget_default: 20,
  worker_tool_budget_min: 2,
  worker_tool_budget_max: 120,
  session_spend_ceiling_nano_usd: 5000000000,
  spend_ceiling_enabled: true,
  spend_soft_stop: true,
  merged_from: ["bundled"],
});

describe("limits field edits", () => {
  it("sends changed fields and explicit inheritance resets", () => {
    expect(changedLimitsFields(sampleLimits(), {
      max_iterations: 500,
      spend_ceiling_enabled: false,
      session_spend_ceiling_nano_usd: null,
    })).toEqual({ spend_ceiling_enabled: false, session_spend_ceiling_nano_usd: null });
  });

  it("adopts host updates without turning unedited fields into overrides", () => createRoot((dispose) => {
    const [limits, setLimits] = createSignal(sampleLimits());
    const draft = createLimitsDraft(limits);
    draft.edit("max_iterations", 600);
    setLimits({ ...sampleLimits(), worker_tool_budget_default: 30 });
    expect(draft.value()?.worker_tool_budget_default).toBe(30);
    expect(draft.value()?.max_iterations).toBe(600);
    expect(draft.beginSave()).toEqual({ max_iterations: 600 });
    dispose();
  }));

  it("preserves a field reverted while its previous edit is being saved", () => createRoot((dispose) => {
    const [limits, setLimits] = createSignal(sampleLimits());
    const draft = createLimitsDraft(limits);
    draft.edit("max_iterations", 600);
    const sent = draft.beginSave();
    draft.edit("max_iterations", 500);
    draft.edit("spend_soft_stop", false);
    draft.acknowledge(sent);
    setLimits({ ...sampleLimits(), max_iterations: 600 });
    draft.finishSave();
    expect(draft.value()?.max_iterations).toBe(500);
    expect(draft.beginSave()).toEqual({ max_iterations: 500, spend_soft_stop: false });
    dispose();
  }));

  it("clears acknowledged edits and ignores a reverted unsaved edit", () => createRoot((dispose) => {
    const [limits, setLimits] = createSignal(sampleLimits());
    const draft = createLimitsDraft(limits);
    draft.edit("max_iterations", 600);
    draft.edit("max_iterations", 500);
    expect(draft.dirty()).toBe(false);
    draft.edit("spend_ceiling_enabled", false);
    const sent = draft.beginSave();
    draft.acknowledge(sent);
    setLimits({ ...sampleLimits(), spend_ceiling_enabled: false });
    draft.finishSave();
    expect(draft.dirty()).toBe(false);
    expect(draft.value()?.spend_ceiling_enabled).toBe(false);
    dispose();
  }));
});
