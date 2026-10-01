import { stubClient } from "../../test/client-fixture.ts";
import { createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { SettingsView } from "./SettingsView.tsx";
import { createAppStore } from "../../store/app-state.ts";
import {
  createSettingsStore,
  INITIAL_SETTINGS_STATE,
} from "../../store/settings-store.ts";
import type { SettingsSection } from "../../settings/settings-nav-model.ts";
import { PROJECT_SETTINGS_OVERLAY_COPY } from "../../settings/security/project-settings-overlay-copy.ts";
import { mockProjectsStore } from "../../test/projects-fixture.ts";

const mockClient = stubClient({
  getHostResources: vi.fn().mockResolvedValue({
    version: 1,
    resources: [],
    diagnostics: [],
    user_catalog_path: "~/.config/paintedwolf/host-resources.yaml",
    checked_at: "2026-08-04T12:00:00Z",
  }),
  refreshHostResources: vi.fn(),
  listProviders: vi.fn(),
  listProviderKinds: vi.fn().mockResolvedValue([]),
  listApprovalGrants: vi.fn().mockResolvedValue({ grants: [] }),
  getLimitsSettings: vi.fn().mockResolvedValue({
    max_iterations: 500,
    overlay_promote_max_iterations: 150,
    max_tool_result_bytes: 524288,
    coordinator_loop: true,
    max_coordinator_loop_cycles: 64,
    llm_turn_timeout_ms: 10800000,
    coordinator_host_turn_timeout_ms: 10800000,
    coordinator_max_sleep_ms: 10800000,
    await_parent_workers_timeout_ms: 1800000,
    worker_tool_budget_default: 20,
    worker_tool_budget_min: 2,
    worker_tool_budget_max: 120,
  }),
});

vi.mock("../../platform/connection/app-connection.ts", () => ({
  connectAppBackend: vi.fn(),
  getLycaonClient: () => mockClient,
}));

describe("SettingsView", () => {
  it("routes the Advanced Host resources tab to the host discovery panel", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const settingsStore = createSettingsStore({ ...INITIAL_SETTINGS_STATE });

    render(() => (
      <SettingsView
        projects={mockProjectsStore()}
        section="debug"
        advancedInitialTab="host_resources"
        appStore={appStore}
        settingsStore={settingsStore}
      />
    ));

    expect(await screen.findByTestId("host-resources-settings-panel")).toBeTruthy();
    expect(
      screen
        .getByTestId("advanced-tab-host_resources")
        .getAttribute("aria-selected"),
    ).toBe("true");
  });

  it("shows an app-settings panel without project scope or models editor", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
    });

    render(() => (
      <SettingsView
        projects={mockProjectsStore()}
        section="debug"
        appStore={appStore}
        settingsStore={settingsStore}
      />
    ));

    expect(await screen.findByTestId("settings-view")).toBeTruthy();
    expect(await screen.findByTestId("debug-settings-panel")).toBeTruthy();
    expect(screen.getByTestId("advanced-tab-budgets")).toBeTruthy();
    expect(screen.getByTestId("advanced-tab-host_resources")).toBeTruthy();
    expect(screen.queryByTestId("advanced-tab-display")).toBeNull();
    expect(screen.queryByTestId("advanced-tab-notifications")).toBeNull();
    expect(screen.queryByTestId("advanced-tab-security")).toBeNull();
    expect(screen.getByTestId("advanced-tab-cache")).toBeTruthy();
    expect(screen.getByTestId("advanced-tab-data")).toBeTruthy();
    expect(screen.getByTestId("advanced-tab-diagnostics")).toBeTruthy();
    expect(screen.queryByTestId("models-editor")).toBeNull();
  });

  it("keeps a stable scroll host across section switches", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
    });
    const [section, setSection] = createSignal<SettingsSection>("approvals");

    render(() => (
      <SettingsView
        projects={mockProjectsStore()}
        section={section()}
        appStore={appStore}
        settingsStore={settingsStore}
      />
    ));

    const bodyOf = (frame: HTMLElement) =>
      frame.querySelector(
        ":scope > .den-scrollport__viewport > .den-scrollport__content > .den-settings-view-body",
      );
    const root = await screen.findByTestId("settings-view");
    expect(root.classList.contains("den-settings-view")).toBe(true);
    const body = bodyOf(root);
    expect(body).not.toBeNull();

    setSection("providers");
    const next = await screen.findByTestId("settings-view");
    expect(next.classList.contains("den-settings-view")).toBe(true);
    expect(bodyOf(next)).toBe(body);
  });

  it("offers a project jump on Approvals when a project is active", async () => {
    const onOpenProjectSettings = vi.fn();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
    });

    render(() => (
      <SettingsView
        projects={mockProjectsStore()}
        section="approvals"
        appStore={appStore}
        settingsStore={settingsStore}
        projectName="Alpha"
        onOpenProjectSettings={onOpenProjectSettings}
      />
    ));

    expect(await screen.findByTestId("approvals-settings")).toBeTruthy();
    const lede = screen.getByTestId("settings-scope-lede");
    expect(lede.getAttribute("data-scope")).toBe("device");
    expect(screen.queryByTestId("settings-scope-chip")).toBeNull();
    expect(screen.getByTestId("settings-scope-counterpart-hint").textContent).toBe(
      PROJECT_SETTINGS_OVERLAY_COPY.counterpartHintFromSettings,
    );
    expect(screen.getByTestId("settings-scope-counterpart").textContent).toContain(
      "Alpha → Approvals",
    );
    fireEvent.click(screen.getByTestId("settings-scope-counterpart"));
    expect(onOpenProjectSettings).toHaveBeenCalledWith("approvals");
  });

  it("exits via the top-right close control", async () => {
    const onClose = vi.fn();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
    });

    render(() => (
      <SettingsView
        projects={mockProjectsStore()}
        section="debug"
        appStore={appStore}
        settingsStore={settingsStore}
        onClose={onClose}
      />
    ));

    expect(await screen.findByTestId("settings-stage-close")).toBeTruthy();
    fireEvent.click(screen.getByTestId("settings-stage-close"));
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("keeps stage chrome mounted across section changes", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const settingsStore = createSettingsStore({ ...INITIAL_SETTINGS_STATE });
    const [section, setSection] = createSignal<SettingsSection>("debug");

    render(() => (
      <SettingsView
        projects={mockProjectsStore()}
        section={section()}
        appStore={appStore}
        settingsStore={settingsStore}
        onClose={() => undefined}
      />
    ));

    const close = await screen.findByTestId("settings-stage-close");
    const panel = close.closest(".den-settings-panel");
    expect(panel?.getAttribute("data-boot")).toBe("ready");
    setSection("approvals");
    expect(screen.getByTestId("settings-stage-close")).toBe(close);
    expect(panel?.getAttribute("data-boot")).toBe("ready");
  });
});
