import { beforeEach, describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import type { Project, SettingsArea, SettingsPricingResponse } from "../../api/types.ts";
import { createAppStore } from "../../store/app-state.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { createCostStore, type CostStore } from "../../store/cost-store.ts";
import { createSettingsStore, type SettingsStore } from "../../store/settings-store.ts";
import { ConnectionInvalidation } from "./connection-invalidation.ts";

const mocks = vi.hoisted(() => ({
  refreshPreflight: vi.fn(() => Promise.resolve()),
  invalidateContributionFrame: vi.fn(() => Promise.resolve()),
  refreshFileSummariesSetting: vi.fn(() => Promise.resolve()),
  invalidateApprovalGrantsCache: vi.fn(),
  refreshProviders: vi.fn(() => Promise.resolve()),
  refreshProviderKinds: vi.fn(() => Promise.resolve()),
  refreshModelPolicy: vi.fn(() => Promise.resolve()),
  refreshPricing: vi.fn(() => Promise.resolve()),
  refreshWorkflowState: vi.fn(() => Promise.resolve()),
  refreshProgress: vi.fn(() => Promise.resolve()),
  refreshCodeScanCache: vi.fn(() => Promise.resolve()),
  settingsScheduler: { schedule: vi.fn(), cancel: vi.fn() },
  sessionSchedulers: {
    scheduleWorkers: vi.fn(),
    scheduleBoard: vi.fn(),
    scheduleCost: vi.fn((refresh: () => Promise<void>) => void refresh()),
    scheduleWorkflow: vi.fn((refresh: () => Promise<void>) => void refresh()),
    scheduleProgress: vi.fn((refresh: () => Promise<void>) => void refresh()),
    cancel: vi.fn(),
  },
  createSettingsInvalidationScheduler: vi.fn(),
  createSessionInvalidationSchedulers: vi.fn(),
}));

vi.mock("../persistence/preflight-store.ts", () => ({ refreshPreflight: mocks.refreshPreflight }));
vi.mock("../../contributions/contribution-store.ts", async (original) => ({
  ...await original<typeof import("../../contributions/contribution-store.ts")>(),
  invalidateContributionFrame: mocks.invalidateContributionFrame,
}));
vi.mock("../../settings/editor/file-summary-settings.ts", async (original) => ({
  ...await original<typeof import("../../settings/editor/file-summary-settings.ts")>(),
  refreshFileSummariesSetting: mocks.refreshFileSummariesSetting,
}));
vi.mock("../../settings/security/approval-grants-cache.ts", async (original) => ({
  ...await original<typeof import("../../settings/security/approval-grants-cache.ts")>(),
  invalidateApprovalGrantsCache: mocks.invalidateApprovalGrantsCache,
}));
vi.mock("../../settings/settings-actions.ts", async (original) => ({
  ...await original<typeof import("../../settings/settings-actions.ts")>(),
  refreshProviders: mocks.refreshProviders,
  refreshProviderKinds: mocks.refreshProviderKinds,
  refreshModelPolicy: mocks.refreshModelPolicy,
  refreshPricing: mocks.refreshPricing,
}));
vi.mock("../../settings/settings-invalidation.ts", () => ({
  createSettingsInvalidationScheduler: mocks.createSettingsInvalidationScheduler,
}));
vi.mock("../../chat/workflow/workflow-actions.ts", () => ({ refreshWorkflowState: mocks.refreshWorkflowState }));
vi.mock("../../chat/progress/progress-actions.ts", () => ({ refreshProgress: mocks.refreshProgress }));
vi.mock("../../chat/session/session-invalidation.ts", () => ({
  createSessionInvalidationSchedulers: mocks.createSessionInvalidationSchedulers,
}));
vi.mock("../../chat/session/session-reconcile.ts", () => ({ refreshCodeScanCache: mocks.refreshCodeScanCache }));

const client = { id: "client" } as unknown as LycaonClient;
const projects: readonly Project[] = [];
const foreground = { kind: "session", project_id: "proj-1", session_id: "session-a" } as const;

function pricing(enabled: boolean): SettingsPricingResponse {
  return { cost_tracking_enabled: enabled, sources: [], available_sources: [] };
}

function foregroundStore(): AppStore {
  const appStore = createAppStore();
  appStore.actions.setCurrentSession({
    id: "session-a",
    owner_person_id: "00000000-0000-4000-8000-000000000002",
    project_id: "proj-1",
    workspace_path: "/tmp/p",
    posture: "build",
    status: "idle",
    created_at: "t",
    activity_at: "t",
    updated_at: "t",
  });
  return appStore;
}

type Setup = {
  client?: LycaonClient | null;
  settings?: SettingsStore | null;
  costs?: CostStore | null;
};

function bound(appStore: AppStore, setup: Setup = {}) {
  const settings = setup.settings === undefined ? createSettingsStore() : setup.settings;
  const costs = setup.costs === undefined ? createCostStore() : setup.costs;
  const currentClient = setup.client === undefined ? client : setup.client;
  const invalidation = new ConnectionInvalidation({
    client: () => currentClient,
    projects: () => projects,
    settings: () => settings,
    costs: () => costs,
  });
  invalidation.bind(appStore);
  return { invalidation, settings, costs };
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.createSettingsInvalidationScheduler.mockReturnValue(mocks.settingsScheduler);
  mocks.createSessionInvalidationSchedulers.mockReturnValue(mocks.sessionSchedulers);
});

describe("connection invalidation", () => {
  it("binds the cost scheduler to live cost consumers and settings to the foreground workspace", () => {
    const appStore = foregroundStore();
    const { costs } = bound(appStore);

    const [, getClient, , isCostLive] = mocks.createSessionInvalidationSchedulers.mock.calls[0]!;
    expect(getClient()).toBe(client);
    expect(isCostLive()).toBe(false);
    const release = costs!.holdLiveRefresh();
    expect(isCostLive()).toBe(true);
    release();
    expect(isCostLive()).toBe(false);

    const [, , , projectDir] = mocks.createSettingsInvalidationScheduler.mock.calls[0]!;
    expect(projectDir()).toBe("/tmp/p");
    appStore.actions.setCurrentSession(undefined);
    expect(projectDir()).toBeUndefined();
  });

  it("rebinding cancels the previous schedulers", () => {
    const appStore = foregroundStore();
    const { invalidation } = bound(appStore);
    invalidation.bind(appStore);
    expect(mocks.sessionSchedulers.cancel).toHaveBeenCalledTimes(1);
    expect(mocks.settingsScheduler.cancel).toHaveBeenCalledTimes(1);
  });

  it("resetting settings leaves session schedulers running", () => {
    const appStore = foregroundStore();
    const { invalidation } = bound(appStore);
    invalidation.resetSettings();
    expect(mocks.settingsScheduler.cancel).toHaveBeenCalledTimes(1);
    expect(mocks.sessionSchedulers.cancel).not.toHaveBeenCalled();

    invalidation.invalidate(appStore, ["providers"], { kind: "device" });
    expect(mocks.settingsScheduler.schedule).not.toHaveBeenCalled();
  });

  it.each<[SettingsArea, readonly string[] | undefined]>([
    ["approvals", ["approvals"]],
    ["limits", ["limits"]],
    ["pricing", ["pricing"]],
    ["review", ["review"]],
    ["power", undefined],
    ["security_scanners", undefined],
    ["web_research", undefined],
  ])("schedules the %s settings slices", (area, slices) => {
    const appStore = foregroundStore();
    const { invalidation } = bound(appStore);
    invalidation.settingsEvent(appStore, { area } as Parameters<ConnectionInvalidation["settingsEvent"]>[1]);
    if (slices) expect(mocks.settingsScheduler.schedule).toHaveBeenCalledWith(slices);
    else expect(mocks.settingsScheduler.schedule).not.toHaveBeenCalled();
  });

  it("applies settings areas that invalidate client caches and revisions", () => {
    const appStore = foregroundStore();
    const { invalidation } = bound(appStore);
    const event = (area: SettingsArea) =>
      invalidation.settingsEvent(appStore, { area } as Parameters<ConnectionInvalidation["settingsEvent"]>[1]);

    event("approvals");
    expect(mocks.invalidateApprovalGrantsCache).toHaveBeenCalledTimes(1);
    expect(appStore.state.approvalsRevision).toBe(1);

    event("extensions");
    expect(appStore.state.extensionsRevision).toBe(1);
    expect(mocks.invalidateContributionFrame).toHaveBeenCalledTimes(1);

    event("mcp");
    expect(mocks.invalidateContributionFrame).toHaveBeenCalledTimes(2);

    event("file_summaries");
    expect(mocks.refreshFileSummariesSetting).toHaveBeenCalledWith(client);

    event("project_trust");
    expect(appStore.state.projectTrustRevision).toBe(1);

    event("verify");
    expect(appStore.state.verifyDetectRevision).toBe(1);
  });

  it("ignores settings events and invalidations without a connected client", () => {
    const appStore = foregroundStore();
    const { invalidation } = bound(appStore, { client: null });
    invalidation.settingsEvent(appStore, { area: "approvals" } as Parameters<ConnectionInvalidation["settingsEvent"]>[1]);
    invalidation.invalidate(appStore, ["providers", "workers", "scan"], foreground);

    expect(mocks.settingsScheduler.schedule).not.toHaveBeenCalled();
    expect(mocks.invalidateApprovalGrantsCache).not.toHaveBeenCalled();
    expect(appStore.state.approvalsRevision).toBe(0);
    expect(mocks.refreshPreflight).not.toHaveBeenCalled();
    expect(mocks.sessionSchedulers.scheduleWorkers).not.toHaveBeenCalled();
    expect(mocks.refreshCodeScanCache).not.toHaveBeenCalled();
  });

  it("refreshes provider settings, readiness, and model policy together", () => {
    const appStore = foregroundStore();
    const { invalidation } = bound(appStore);

    invalidation.invalidate(appStore, ["providers", "model_policy"], { kind: "device" });
    expect(mocks.settingsScheduler.schedule).toHaveBeenCalledWith(["providers", "model_policy"]);
    expect(mocks.refreshPreflight).toHaveBeenCalledTimes(1);
    expect(appStore.state.modelPolicyRevision).toBe(1);

    invalidation.invalidate(appStore, ["model_policy"], { kind: "device" });
    expect(mocks.settingsScheduler.schedule).toHaveBeenLastCalledWith(["model_policy"]);
    expect(mocks.refreshPreflight).toHaveBeenCalledTimes(1);

    invalidation.invalidate(appStore, ["readiness"], { kind: "device" });
    expect(mocks.refreshPreflight).toHaveBeenCalledTimes(2);
    expect(mocks.settingsScheduler.schedule).toHaveBeenCalledTimes(2);
    expect(appStore.state.modelPolicyRevision).toBe(2);
  });

  it("refreshes the foreground session projections its keys name", async () => {
    const appStore = foregroundStore();
    const { invalidation } = bound(appStore);

    invalidation.invalidate(appStore, ["workers", "workflows", "session", "board", "scan"], foreground);

    expect(mocks.sessionSchedulers.scheduleWorkers).toHaveBeenCalledWith("/tmp/p", "session-a");
    expect(mocks.refreshWorkflowState).toHaveBeenCalledWith(appStore, client, "session-a", "/tmp/p", projects);
    expect(mocks.refreshProgress).toHaveBeenCalledWith(appStore, client, "session-a");
    expect(mocks.sessionSchedulers.scheduleBoard).toHaveBeenCalledWith("/tmp/p", "session-a", false);
    expect(mocks.refreshCodeScanCache).toHaveBeenCalledWith(appStore, client);
    expect(mocks.sessionSchedulers.scheduleCost).not.toHaveBeenCalled();
  });

  it("contains failed workflow, progress, and scan refreshes", async () => {
    mocks.refreshWorkflowState.mockRejectedValueOnce(new Error("gone"));
    mocks.refreshProgress.mockRejectedValueOnce(new Error("gone"));
    mocks.refreshCodeScanCache.mockRejectedValueOnce(new Error("gone"));
    const appStore = foregroundStore();
    const { invalidation } = bound(appStore);

    invalidation.invalidate(appStore, ["workflows", "session", "scan"], foreground);
    await Promise.resolve();
    await Promise.resolve();

    expect(mocks.refreshWorkflowState).toHaveBeenCalledTimes(1);
    expect(mocks.refreshProgress).toHaveBeenCalledTimes(1);
  });

  it("refreshes session cost with the board only while cost tracking is enabled", async () => {
    const appStore = foregroundStore();
    const settings = createSettingsStore();
    const costs = createCostStore();
    const refreshSession = vi.spyOn(costs, "refreshSession").mockResolvedValue(undefined);
    const invalidated = vi.fn();
    costs.onInvalidated(invalidated);
    const { invalidation } = bound(appStore, { settings, costs });

    invalidation.invalidate(appStore, ["board", "cost"], foreground);
    expect(mocks.sessionSchedulers.scheduleBoard).toHaveBeenCalledWith("/tmp/p", "session-a", true);
    expect(mocks.sessionSchedulers.scheduleCost).not.toHaveBeenCalled();

    settings.actions.setPricing(pricing(true));
    invalidation.invalidate(appStore, ["cost"], foreground);
    await vi.waitFor(() => expect(invalidated).toHaveBeenCalledTimes(1));
    expect(refreshSession).toHaveBeenCalledWith(client, "session-a");
  });

  it("applies device keys but not session keys from a background session", () => {
    const appStore = foregroundStore();
    const { invalidation } = bound(appStore);

    invalidation.invalidate(appStore, ["providers", "workers", "workflows", "scan"], {
      kind: "session",
      project_id: "proj-1",
      session_id: "session-b",
    });

    expect(mocks.refreshPreflight).toHaveBeenCalledTimes(1);
    expect(mocks.sessionSchedulers.scheduleWorkers).not.toHaveBeenCalled();
    expect(mocks.refreshWorkflowState).not.toHaveBeenCalled();
    expect(mocks.refreshCodeScanCache).not.toHaveBeenCalled();
  });

  it("skips workspace projections when no workspace is foreground", () => {
    const appStore = createAppStore();
    const { invalidation } = bound(appStore);
    invalidation.invalidate(appStore, ["workers", "board", "scan"], { kind: "device" });
    expect(mocks.sessionSchedulers.scheduleWorkers).not.toHaveBeenCalled();
    expect(mocks.sessionSchedulers.scheduleBoard).not.toHaveBeenCalled();
    expect(mocks.refreshCodeScanCache).not.toHaveBeenCalled();
  });

  it("hydrates every settings slice and tolerates a failed read", async () => {
    const appStore = foregroundStore();
    const { invalidation, settings } = bound(appStore);
    const shouldApply = () => true;
    mocks.refreshPricing.mockRejectedValueOnce(new Error("offline"));

    await expect(invalidation.hydrate(client, shouldApply)).resolves.toBeUndefined();
    expect(mocks.refreshProviders).toHaveBeenCalledWith(settings, client, shouldApply);
    expect(mocks.refreshProviderKinds).toHaveBeenCalledWith(settings, client, shouldApply);
    expect(mocks.refreshModelPolicy).toHaveBeenCalledWith(settings, client, projects, shouldApply);
    expect(mocks.refreshPricing).toHaveBeenCalledWith(settings, client, shouldApply);
  });

  it("does not hydrate or schedule settings without a settings store", async () => {
    const appStore = foregroundStore();
    const { invalidation } = bound(appStore, { settings: null });
    expect(mocks.createSettingsInvalidationScheduler).not.toHaveBeenCalled();

    await invalidation.hydrate(client, () => true);
    expect(mocks.refreshProviders).not.toHaveBeenCalled();
  });
});
