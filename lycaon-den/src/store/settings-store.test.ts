import { describe, expect, it, vi } from "vitest";
import { createSettingsStore, type SettingsState } from "./settings-store.ts";
import {
  createSettingsInvalidationScheduler,
  SETTINGS_INVALIDATION_MS,
} from "../settings/settings-invalidation.ts";
import { emptyProjects } from "../test/projects-fixture.ts";
import { stubClient } from "../test/client-fixture.ts";
import { loadSettingsPanel, refreshSettingsSlices } from "../settings/settings-actions.ts";
import type {
  ApprovalConfigResponse,
  ModelPolicy,
  SettingsLimitsResponse,
  SettingsPricingResponse,
} from "../api/types.ts";

describe("scoped settings projections", () => {
  it("settles unresolved project scope as a retryable panel error", async () => {
    const store = createSettingsStore();
    const client = stubClient();
    await loadSettingsPanel(store, client, [], "providers", "/tmp/missing", { alwaysProjectScope: true });
    expect(store.panelReady(client, "providers")).toBe(true);
    expect(store.panelError(client, "providers")).toContain("Project settings cannot resolve");
  });

  it("keeps project policy separate while sharing device providers", async () => {
    const device = createSettingsStore();
    const a = createSettingsStore(undefined, device);
    const b = createSettingsStore(undefined, device);
    const client = {};
    const policy = (model: string): ModelPolicy => ({
      coordinator: { provider_id: "local", model }, lite: { provider_id: "local", model },
      agent_pool: { selection: "round_robin", models: [] },
    });
    await Promise.all([
      a.load(client, "providers", async () => ({ modelPolicy: policy("a"), providers: [] })),
      b.load(client, "providers", async () => ({ modelPolicy: policy("b"), providers: [] })),
    ]);
    expect(a.state.modelPolicy?.coordinator?.model).toBe("a");
    expect(b.state.modelPolicy?.coordinator?.model).toBe("b");
    expect(device.state.modelPolicy).toBeUndefined();
    expect(a.state.providers).toBe(device.state.providers);
  });

  it("does not install an old client's response after the connection changes", async () => {
    const store = createSettingsStore();
    let finish!: (snapshot: Partial<SettingsState>) => void;
    const old = store.load({}, "providers", () => new Promise((resolve) => { finish = resolve; }));
    await Promise.resolve();
    const modelPolicy: ModelPolicy = {
      coordinator: { provider_id: "local", model: "current" },
      lite: { provider_id: "local", model: "current" },
      agent_pool: { selection: "round_robin", models: [] },
    };
    await store.load({}, "providers", async () => ({ providers: [], modelPolicy }));
    finish({ providers: [], modelPolicy: { ...modelPolicy, coordinator: { provider_id: "local", model: "obsolete" } } });
    await old;
    expect(store.state.modelPolicy?.coordinator?.model).toBe("current");
    expect(store.state.error).toBeUndefined();
  });
});

describe("settings invalidation", () => {
  it("debounces providers and model_policy refetch", async () => {
    vi.useFakeTimers();
    const settingsStore = createSettingsStore();
    const listProviders = vi.fn().mockResolvedValue([]);
    const getModelPolicySettings = vi.fn().mockResolvedValue({
      coordinator: { provider_id: "openai", model: "gpt-4o" },
      lite: { provider_id: "openai", model: "gpt-4o-mini" },
      agent_pool: { selection: "round_robin", models: [] },
    });

    const scheduler = createSettingsInvalidationScheduler(
      settingsStore,
      () =>
        ({
          listProviders,
          getModelPolicySettings,
          getApprovalsSettings: vi.fn(),
          getLimitsSettings: vi.fn(),
          getPricingSettings: vi.fn().mockResolvedValue({
            cost_tracking_enabled: false,
            sources: [],
            available_sources: [],
          }),
        }) as never,
      () => emptyProjects,
      () => undefined,
    );

    scheduler.schedule(["providers", "model_policy"]);
    expect(listProviders).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(SETTINGS_INVALIDATION_MS);
    expect(listProviders).toHaveBeenCalledOnce();
    expect(getModelPolicySettings).toHaveBeenCalledOnce();

    scheduler.cancel();
    vi.useRealTimers();
  });
});

describe("setApprovals", () => {
  it("clears field_sources and defaults when a device-scope response omits them", () => {
    const settingsStore = createSettingsStore();
    const projectScoped: ApprovalConfigResponse = {
      scope: "project",
      rules: [],
      managed_rules: [],
      merged_from: ["project", "global"],
      approval_posture: "balanced",
      ai_rationale_enabled: true,
      never_ask: false,
      field_sources: {
        approval_posture: "override",
        ai_rationale_enabled: "default",
        never_ask: "default",
      },
      defaults: {
        approval_posture: "light",
        ai_rationale_enabled: true,
        never_ask: false,
      },
    };
    settingsStore.actions.setApprovals(projectScoped);
    expect(settingsStore.state.approvals?.field_sources).toEqual({
      approval_posture: "override",
      ai_rationale_enabled: "default",
      never_ask: "default",
    });
    expect(settingsStore.state.approvals?.defaults).toEqual({
      approval_posture: "light",
      ai_rationale_enabled: true,
      never_ask: false,
    });

    // Scope changes clear fields omitted by the new response.
    const deviceScoped: ApprovalConfigResponse = {
      scope: "global",
      rules: [],
      managed_rules: [],
      merged_from: ["global"],
      approval_posture: "strict",
      ai_rationale_enabled: true,
      never_ask: false,
    };
    settingsStore.actions.setApprovals(deviceScoped);

    expect(settingsStore.state.approvals?.scope).toBe("global");
    expect(settingsStore.state.approvals?.approval_posture).toBe("strict");
    expect(settingsStore.state.approvals?.field_sources).toBeUndefined();
    expect(settingsStore.state.approvals?.defaults).toBeUndefined();
  });
});

function limitsFixture(
  overrides: Partial<SettingsLimitsResponse> = {},
): SettingsLimitsResponse {
  return {
    scope: "project",
    max_iterations: 40,
    overlay_promote_max_iterations: 10,
    max_tool_result_bytes: 100000,
    llm_turn_timeout_ms: 120000,
    coordinator_host_turn_timeout_ms: 600000,
    coordinator_max_sleep_ms: 30000,
    await_parent_workers_timeout_ms: 600000,
    worker_tool_budget_default: 30,
    worker_tool_budget_min: 2,
    worker_tool_budget_max: 100,
    spend_soft_stop: true,
    merged_from: ["project", "global"],
    ...overrides,
  };
}

describe("setLimits", () => {
  it("clears the spend-ceiling fields when a later response omits them", () => {
    const settingsStore = createSettingsStore();
    settingsStore.actions.setLimits(
      limitsFixture({
        session_spend_ceiling_nano_usd: 5000000000,
        spend_warning_ratio: 0.8,
        spend_ceiling_enabled: true,
      }),
    );
    expect(settingsStore.state.limits?.session_spend_ceiling_nano_usd).toBe(5000000000);
    expect(settingsStore.state.limits?.spend_ceiling_enabled).toBe(true);

    // Removing the override clears the absent spend ceiling.
    settingsStore.actions.setLimits(limitsFixture({ scope: "global" }));
    expect(settingsStore.state.limits?.scope).toBe("global");
    expect(settingsStore.state.limits?.session_spend_ceiling_nano_usd).toBeUndefined();
    expect(settingsStore.state.limits?.spend_warning_ratio).toBeUndefined();
    expect(settingsStore.state.limits?.spend_ceiling_enabled).toBeUndefined();
  });
});

describe("setPricing", () => {
  it("clears cost_tracking_since on an on-to-off flip", () => {
    const settingsStore = createSettingsStore();
    const trackingOn: SettingsPricingResponse = {
      cost_tracking_enabled: true,
      cost_tracking_since_at: "2026-08-01T00:00:00Z",
      sources: [],
      available_sources: [],
    };
    settingsStore.actions.setPricing(trackingOn);
    expect(settingsStore.state.pricing?.cost_tracking_since_at).toBe(
      "2026-08-01T00:00:00Z",
    );

    // Disabled tracking omits cost_tracking_since.
    const trackingOff: SettingsPricingResponse = {
      cost_tracking_enabled: false,
      sources: [],
      available_sources: [],
    };
    settingsStore.actions.setPricing(trackingOff);
    expect(settingsStore.state.pricing?.cost_tracking_enabled).toBe(false);
    expect(settingsStore.state.pricing?.cost_tracking_since_at).toBeUndefined();
  });
});

describe("setModelPolicy", () => {
  it("drops a removed pool row instead of leaving it stale", () => {
    const settingsStore = createSettingsStore();
    const withTwoWorkers: ModelPolicy = {
      coordinator: { provider_id: "openai", model: "gpt-4o-mini" },
      lite: { provider_id: "openai", model: "gpt-4o-mini" },
      agent_pool: {
        selection: "round_robin",
        models: [
          { provider_id: "openai", model: "gpt-4o-mini" },
          { provider_id: "ollama", model: "llama3.1" },
        ],
      },
    };
    settingsStore.actions.setModelPolicy(withTwoWorkers);
    expect(settingsStore.state.modelPolicy?.agent_pool.models).toHaveLength(2);

    const withOneWorker: ModelPolicy = {
      coordinator: { provider_id: "openai", model: "gpt-4o-mini" },
      lite: { provider_id: "ollama", model: "llama3.1" },
      agent_pool: {
        selection: "round_robin",
        models: [{ provider_id: "openai", model: "gpt-4o-mini" }],
      },
    };
    settingsStore.actions.setModelPolicy(withOneWorker);

    expect(settingsStore.state.modelPolicy?.lite).toEqual({
      provider_id: "ollama",
      model: "llama3.1",
    });
    expect(settingsStore.state.modelPolicy?.agent_pool.models).toEqual([
      { provider_id: "openai", model: "gpt-4o-mini" },
    ]);
  });
});


describe("effective limits invalidation", () => {
  it("refreshes the device and active project after a limits event", async () => {
    vi.useFakeTimers();
    try {
      const store = createSettingsStore();
      store.actions.setEffectiveLimits("project-a", limitsFixture({ session_spend_ceiling_nano_usd: 1230000000 }));
      const getLimitsSettings = vi.fn(async (projectId?: string) =>
        limitsFixture({ scope: projectId ? "project" : "global", session_spend_ceiling_nano_usd: projectId ? 1240000000 : 5000000000 }));
      const client = stubClient({ getLimitsSettings });
      const scheduler = createSettingsInvalidationScheduler(store, () => client, () => [], () => "/worktree/path");
      scheduler.schedule(["limits"]);
      await vi.advanceTimersByTimeAsync(SETTINGS_INVALIDATION_MS);
      expect(store.state.limits?.session_spend_ceiling_nano_usd).toBe(5000000000);
      expect(store.state.effectiveLimits).toMatchObject({ projectId: "project-a", limits: { session_spend_ceiling_nano_usd: 1240000000 } });
      expect(getLimitsSettings).toHaveBeenCalledWith("project-a");
      scheduler.cancel();
    } finally { vi.useRealTimers(); }
  });

  it.each(["project switch", "newer refresh"] as const)("discards a late effective projection after %s", async (change) => {
    const store = createSettingsStore();
    store.actions.setEffectiveLimits("project-a", limitsFixture({ session_spend_ceiling_nano_usd: 1000000000 }));
    let finish!: (value: SettingsLimitsResponse) => void;
    const held = new Promise<SettingsLimitsResponse>((resolve) => { finish = resolve; });
    const client = stubClient({ getLimitsSettings: vi.fn(async (projectId?: string) =>
      projectId ? held : limitsFixture({ scope: "global" })) });
    const old = refreshSettingsSlices(store, client, [], ["limits"]);
    if (change === "project switch") {
      store.actions.setEffectiveLimits("project-b", limitsFixture({ session_spend_ceiling_nano_usd: 3000000000 }));
    } else {
      await refreshSettingsSlices(store, stubClient({ getLimitsSettings: vi.fn(async () =>
        limitsFixture({ session_spend_ceiling_nano_usd: 3000000000 })) }), [], ["limits"]);
    }
    finish(limitsFixture({ session_spend_ceiling_nano_usd: 2000000000 }));
    await old;
    expect(store.state.effectiveLimits?.projectId).toBe(change === "project switch" ? "project-b" : "project-a");
    expect(store.state.effectiveLimits?.limits.session_spend_ceiling_nano_usd).toBe(3000000000);
  });

  it("does not fabricate project limits when no project projection is active", async () => {
    const store = createSettingsStore();
    const getLimitsSettings = vi.fn(async () => limitsFixture({ scope: "global" }));
    await refreshSettingsSlices(store, stubClient({ getLimitsSettings }), [], ["limits"]);
    expect(getLimitsSettings).toHaveBeenCalledExactlyOnceWith();
    expect(store.state.effectiveLimits).toBeUndefined();
  });
});
