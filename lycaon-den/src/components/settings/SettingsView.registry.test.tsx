import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { REVEAL_FLASH_CLASS } from "../../ui/reveal-flash.ts";
import { SettingsView } from "./SettingsView.tsx";
import { stubClient } from "../../test/client-fixture.ts";
import { providerModel } from "../../test/provider-fixtures.ts";
import { mockProjectsStore } from "../../test/projects-fixture.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createSettingsStore, INITIAL_SETTINGS_STATE } from "../../store/settings-store.ts";
import { resetAppStateSnapshotForTests } from "../../store/app-state-snapshot.ts";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";
import {
  resetContributionStoreForTest,
  seedContributionFrameForTest,
} from "../../contributions/contribution-store.ts";
import { STOCK_FRAME } from "../../contributions/stock-frame.generated.ts";
import { WEB_RESEARCH_CATALOG } from "../../settings/web-research-catalog.generated.ts";
import {
  SETTINGS_REGISTRY,
  SETTING_ANCHOR_ATTRIBUTE,
  settingDefinition,
  type SettingDefinition,
} from "../../settings/settings-registry.ts";
import {
  pendingSettingReveal,
  requestSettingReveal,
  settleSettingReveal,
} from "../../settings/settings-reveal.ts";
import type {
  AdvancedSettingsTab,
  GeneralSettingsTab,
  SettingsSection,
} from "../../settings/settings-nav-model.ts";

vi.mock("../../platform/persistence/app-state.ts", () => ({
  loadAppState: vi.fn(async () => ({ ...EMPTY_APP_STATE_V1 })),
  patchAppState: vi.fn(async (_patch: unknown, fallback: unknown) => fallback),
}));

vi.mock("../../platform/files/detect-editors.ts", () => ({
  detectEditors: vi.fn(async () => []),
}));

// Updates and Opening files render only where the app shares this device.
vi.mock("../../platform/runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../platform/runtime.ts")>()),
  isTauriRuntime: () => true,
  tauriPlatform: () => "macos",
}));

vi.mock("../../platform/connection/host-identity.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../platform/connection/host-identity.ts")>()),
  hostSharesDevice: () => true,
}));

const readyProvider = {
  id: "openai",
  kind: "openai",
  label: "OpenAI",
  base_url: "https://api.openai.com/v1",
  configured: true,
  ready_to_assign: true,
  credential_present: true,
  requires_api_key: true,
  features: { tool_calls: true, thinking: true, prompt_cache: "automatic_prefix" },
  models: [providerModel("gpt-4o")],
};

const client = stubClient({
  getFileSummariesSettings: vi.fn(async () => ({ enabled: true })),
  getPowerSettings: vi.fn(async () => ({
    supported: true,
    keep_awake_while_working: true,
    inhibiting: false,
  })),
  listProviders: vi.fn(async () => [readyProvider]),
  listProviderKinds: vi.fn(async () => []),
  getModelPolicySettings: vi.fn(async () => ({
    coordinator: { provider_id: "openai", model: "gpt-4o" },
    lite: { provider_id: "openai", model: "gpt-4o" },
    agent_pool: { selection: "first", models: [] },
  })),
  getApprovalsSettings: vi.fn(async () => ({
    scope: "global",
    rules: [],
    managed_rules: [],
    merged_from: [],
    approval_posture: "balanced",
    ai_rationale_enabled: true,
    never_ask: false,
  })),
  listMcpProviders: vi.fn(async () => []),
  listMcpRecipes: vi.fn(async () => ({ recipes: [] })),
  getSecurityScannersSettings: vi.fn(async () => ({
    enabled: true,
    landed_change_scope: "path_scoped",
    source_verify: "stat",
  })),
  listScanners: vi.fn(async () => ({
    scanners: [],
    rejected: [],
    user_scanners_path: "/tmp/scanners.yaml",
  })),
  listScannerCatalog: vi.fn(async () => ({ scanners: [] })),
  getWebResearchProviders: vi.fn(async () => ({
    warming: true,
    guess_domains: true,
    search_enabled: true,
    direct: {
      configured: true,
      card: { provider_id: "direct", kind: "direct", label: "Direct search" },
    },
    providers: WEB_RESEARCH_CATALOG.slice(0, 1).map((entry) => ({
      id: entry.id,
      kind: entry.kind,
      label: entry.label,
      roles: [...entry.roles],
      default_enabled: entry.default_enabled,
      configured: true,
      credential_present: true,
      credential_source: "stored",
      credential_slot: entry.credential_slot,
    })),
    enabled_providers: ["direct"],
  })),
  getWebResearchIndex: vi.fn(async () => ({ available: false })),
  getPricingSettings: vi.fn(async () => ({
    cost_tracking_enabled: true,
    sources: [],
    available_sources: [],
  })),
  getLimitsSettings: vi.fn(async () => ({
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
  })),
  getLocalData: vi.fn(async () => ({
    buckets: [{ id: "web_index", present: true, bytes: 2048 }],
    workspace_caches: [],
  })),
  getHistoryStorage: vi.fn(async () => ({
    policy: {
      version: 1,
      revision: 1,
      suspended: false,
      recordings: { mode: "forever" },
      checkpoints: { mode: "forever" },
      source_revisions: { mode: "forever" },
      scan_detail: { mode: "forever" },
      receipt_detail: { mode: "forever" },
    },
    lanes: [],
    protections: [],
  })),
  listProjects: vi.fn(async () => []),
});

vi.mock("../../platform/connection/app-connection.ts", () => ({
  connectAppBackend: vi.fn(),
  getLycaonClient: () => client,
}));

type Location = {
  key: string;
  section: SettingDefinition["section"];
  tab?: string;
  settings: SettingDefinition[];
};

function locations(): Location[] {
  const byKey = new Map<string, Location>();
  for (const setting of SETTINGS_REGISTRY) {
    const key = setting.tab ? `${setting.section} · ${setting.tab}` : setting.section;
    const location =
      byKey.get(key) ?? { key, section: setting.section, tab: setting.tab, settings: [] };
    location.settings.push(setting);
    byKey.set(key, location);
  }
  return [...byKey.values()];
}

const HIDING_ANCESTOR = "[inert], [hidden], [aria-hidden='true']";

function presentedAnchors(container: HTMLElement): HTMLElement[] {
  return [
    ...container.querySelectorAll<HTMLElement>(`[${SETTING_ANCHOR_ATTRIBUTE}]`),
  ].filter((anchor) => !anchor.closest(HIDING_ANCESTOR));
}

beforeEach(() => {
  resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
  seedContributionFrameForTest(STOCK_FRAME);
  Element.prototype.scrollIntoView = vi.fn();
});

afterEach(() => {
  const request = pendingSettingReveal();
  if (request) settleSettingReveal(request);
  cleanup();
  resetContributionStoreForTest();
});

describe("SettingsView and the settings registry", () => {
  for (const location of locations()) {
    it(`renders each ${location.key} setting in exactly one labelled row`, async () => {
      const appStore = createAppStore();
      appStore.actions.setSidecarStatus("connected");
      // The reveal request selects tabs that have no initial-tab prop.
      const settingsStore = createSettingsStore({ ...INITIAL_SETTINGS_STATE });
      const [first] = location.settings;
      if (first) requestSettingReveal(first);
      const { container } = render(() => (
        <SettingsView
          projects={mockProjectsStore()}
          section={location.section}
          generalInitialTab={
            location.section === "general" ? (location.tab as GeneralSettingsTab) : undefined
          }
          advancedInitialTab={
            location.section === "debug" ? (location.tab as AdvancedSettingsTab) : undefined
          }
          appStore={appStore}
          settingsStore={settingsStore}
        />
      ));

      const expected = location.settings.map((setting) => setting.id);
      await waitFor(
        () => {
          const rendered = new Set(
            presentedAnchors(container).map((a) => a.getAttribute(SETTING_ANCHOR_ATTRIBUTE)),
          );
          expect(expected.filter((id) => !rendered.has(id))).toEqual([]);
        },
        { timeout: 5000 },
      );

      const anchors = presentedAnchors(container);
      for (const setting of location.settings) {
        const rows = anchors.filter(
          (anchor) => anchor.getAttribute(SETTING_ANCHOR_ATTRIBUTE) === setting.id,
        );
        expect(rows, setting.id).toHaveLength(1);
        const [row] = rows;
        const named =
          row?.textContent?.includes(setting.label) ||
          row?.getAttribute("aria-label") === setting.label;
        expect(named, `${setting.id} shows "${setting.label}"`).toBe(true);
      }
      const strays = anchors
        .map((anchor) => anchor.getAttribute(SETTING_ANCHOR_ATTRIBUTE))
        .filter((id) => !expected.includes(id as never));
      expect(strays).toEqual([]);
    });
  }
});

describe("SettingsView setting reveal", () => {
  function renderAt(section: () => SettingsSection) {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const settingsStore = createSettingsStore({ ...INITIAL_SETTINGS_STATE });
    return render(() => (
      <SettingsView
        projects={mockProjectsStore()}
        section={section()}
        appStore={appStore}
        settingsStore={settingsStore}
      />
    ));
  }

  function anchor(container: HTMLElement, id: string): HTMLElement | null {
    return container.querySelector<HTMLElement>(`[${SETTING_ANCHOR_ATTRIBUTE}="${id}"]`);
  }

  it("lands on a row in another section and tab while Settings is open", async () => {
    const [section, setSection] = createSignal<SettingsSection>("cost");
    const { container } = renderAt(section);
    await screen.findByTestId("cost-sources");

    setSection("general");
    requestSettingReveal(settingDefinition("editor-font-size"));

    await waitFor(
      () =>
        expect(
          anchor(container, "editor-font-size")?.classList.contains(REVEAL_FLASH_CLASS),
        ).toBe(true),
      { timeout: 5000 },
    );
    expect(screen.getByTestId("general-tab-editor").getAttribute("aria-selected")).toBe("true");
    expect(document.activeElement).toBe(screen.getByTestId("editor-font-size-dec"));
    expect(Element.prototype.scrollIntoView).toHaveBeenCalled();
    expect(pendingSettingReveal()).toBeNull();
  });

  it("lands on a row when Settings opens after the request", async () => {
    requestSettingReveal(settingDefinition("never-ask"));
    const { container } = renderAt(() => "debug");

    await waitFor(
      () =>
        expect(anchor(container, "never-ask")?.classList.contains(REVEAL_FLASH_CLASS)).toBe(
          true,
        ),
      { timeout: 5000 },
    );
    expect(screen.getByTestId("advanced-tab-approvals").getAttribute("aria-selected")).toBe(
      "true",
    );
    expect(document.activeElement).toBe(screen.getByTestId("approvals-off-start"));
  });

  it("leaves a request for another section pending", async () => {
    const request = requestSettingReveal(settingDefinition("cost-tracking"));
    renderAt(() => "general");
    await screen.findByTestId("general-settings-panel");
    expect(pendingSettingReveal()).toBe(request);
  });
});

