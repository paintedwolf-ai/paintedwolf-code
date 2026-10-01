import { stubClient } from "../../test/client-fixture.ts";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import { createAppStore } from "../../store/app-state.ts";
import {
  createSettingsStore,
  INITIAL_SETTINGS_STATE,
} from "../../store/settings-store.ts";
import type { ModelPolicy } from "../../api/types.ts";
import { MODELS_SETTINGS_COPY } from "../../settings/providers/models-settings-copy.ts";
import { APPROVALS_SETTINGS_COPY } from "../../settings/security/approvals-settings-copy.ts";
import { PROJECT_SETTINGS_OVERLAY_COPY } from "../../settings/security/project-settings-overlay-copy.ts";
import type { ProjectContextSection } from "../../settings/settings-nav-model.ts";
import { ProjectContextView } from "./ProjectContextView.tsx";
import { mockProjectsStore } from "../../test/projects-fixture.ts";

const emptyPolicy: ModelPolicy = {
  coordinator: { provider_id: "", model: "" },
  lite: { provider_id: "", model: "" },
  agent_pool: { selection: "round_robin", models: [] },
};

const mockClient = stubClient({
  listProviders: vi.fn(async () => []),
  getModelPolicySettings: vi.fn(async () => ({ assignments: {}, worker_pool: false })),
  updateModelPolicySettings: vi.fn(async (policy) => policy),
  getProjectTrust: vi.fn(async () => ({
    project_id: "proj-1",
    surfaces: [],

    unread_count: 0,
    review: { id: "review", project_id: "proj-1", changes: [] },

  })),
  getApprovalsSettings: vi.fn(async () => ({
    scope: "project",
    rules: [],
    merged_from: [],
    approval_posture: "balanced",
    ai_rationale_enabled: true,
      never_ask: false,
    field_sources: {
      approval_posture: "default",
      ai_rationale_enabled: "default",
      never_ask: "default",
    },
    defaults: {
      approval_posture: "balanced",
      ai_rationale_enabled: true,
      never_ask: false,
    },
  })),
  updateApprovalsSettings: vi.fn(async (req) => req),
  listMcpProviders: vi.fn(async () => []),
});

vi.mock("../../platform/connection/app-connection.ts", () => ({
  connectAppBackend: vi.fn(),
  getLycaonClient: () => mockClient,
}));

describe("ProjectContextView", () => {
  it("renders models editor with project-scope lede and Settings jump", async () => {
    const onOpenDeviceSettings = vi.fn();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
      modelPolicy: emptyPolicy,
      providers: [],
    });

    render(() => (
      <ProjectContextView
        projects={mockProjectsStore()}
        section="providers"
        onSectionChange={vi.fn()}
        projectId="proj-1"
        projectDir="/tmp/demo"
        appStore={appStore}
        settingsStore={settingsStore}
        onOpenDeviceSettings={onOpenDeviceSettings}
      />
    ));

    expect(await screen.findByTestId("models-editor")).toBeTruthy();
    expect(screen.getByTestId("project-configuration-header")).toBeTruthy();
    expect(screen.getByTestId("project-context-entry-providers").getAttribute("data-active")).toBe(
      "true",
    );
    const lede = screen.getByTestId("settings-scope-lede");
    expect(lede.getAttribute("data-scope")).toBe("project");
    expect(screen.queryByTestId("settings-scope-chip")).toBeNull();
    expect(lede.textContent).toContain(MODELS_SETTINGS_COPY.modelPolicyIntro);
    expect(screen.getByTestId("settings-scope-counterpart-hint").textContent).toBe(
      PROJECT_SETTINGS_OVERLAY_COPY.counterpartHintFromProject,
    );
    expect(screen.getByTestId("settings-scope-counterpart").textContent).toContain(
      "Settings → AI providers",
    );
    fireEvent.click(screen.getByTestId("settings-scope-counterpart"));
    expect(onOpenDeviceSettings).toHaveBeenCalledWith("providers");
    expect(screen.getByTestId("settings-scope-badge").textContent).toBe("Project");
    expect(screen.getByTestId("project-settings-override")).toBeTruthy();
  });

  it("shows the same Approvals intro as Settings with a Settings jump", async () => {
    const onOpenDeviceSettings = vi.fn();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
      approvals: {
        scope: "project",
        rules: [],
        managed_rules: [],
        merged_from: [],
        approval_posture: "balanced",
        ai_rationale_enabled: true,
        never_ask: false,
        field_sources: {
          approval_posture: "default",
          ai_rationale_enabled: "default",
          never_ask: "default",
        },
        defaults: {
          approval_posture: "balanced",
          ai_rationale_enabled: true,
          never_ask: false,
        },
      },
    });

    render(() => (
      <ProjectContextView
        projects={mockProjectsStore()}
        section="approvals"
        onSectionChange={vi.fn()}
        projectId="proj-1"
        projectDir="/tmp/demo"
        appStore={appStore}
        settingsStore={settingsStore}
        onOpenDeviceSettings={onOpenDeviceSettings}
      />
    ));

    expect(await screen.findByTestId("approvals-settings")).toBeTruthy();
    fireEvent.click(screen.getByTestId("approvals-tab-ask"));
    const lede = screen.getByTestId("settings-scope-lede");
    expect(lede.getAttribute("data-scope")).toBe("project");
    expect(lede.textContent).toContain(APPROVALS_SETTINGS_COPY.intro);
    fireEvent.click(screen.getByTestId("settings-scope-counterpart"));
    expect(onOpenDeviceSettings).toHaveBeenCalledWith("approvals");
  });

  it("exits via the top-right close control", async () => {
    const onClose = vi.fn();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
      modelPolicy: emptyPolicy,
      providers: [],
    });

    render(() => (
      <ProjectContextView
        projects={mockProjectsStore()}
        section="providers"
        onSectionChange={vi.fn()}
        projectId="proj-1"
        projectDir="/tmp/demo"
        appStore={appStore}
        settingsStore={settingsStore}
        onClose={onClose}
      />
    ));

    expect(await screen.findByTestId("settings-stage-close")).toBeTruthy();
    fireEvent.click(screen.getByTestId("settings-stage-close"));
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("keeps the header, tabs, and close control mounted across a tab change", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
      modelPolicy: emptyPolicy,
      providers: [],
    });
    const [section, setSection] = createSignal<ProjectContextSection>("providers");

    render(() => (
      <ProjectContextView
        projects={mockProjectsStore()}
        section={section()}
        onSectionChange={setSection}
        projectId="proj-1"
        projectDir="/tmp/demo"
        appStore={appStore}
        settingsStore={settingsStore}
        onClose={vi.fn()}
      />
    ));

    const header = await screen.findByTestId("project-configuration-header");
    const panel = header.closest(".den-settings-panel");
    expect(panel).toBeTruthy();
    const bootBefore = panel!.getAttribute("data-boot");

    fireEvent.click(screen.getByTestId("project-context-entry-approvals"));

    // Retained tabs preserve element identity.
    expect(screen.getByTestId("project-configuration-header")).toBe(header);
    expect(screen.getByTestId("settings-stage-close").closest(".den-settings-panel")).toBe(
      panel,
    );
    expect(screen.getByTestId("project-context-entry-approvals").getAttribute("data-active")).toBe(
      "true",
    );
    // Tab changes retain the prepared stage.
    expect(panel!.getAttribute("data-boot")).toBe(bootBefore);
    expect(await screen.findByTestId("approvals-settings")).toBeTruthy();
  });
});
