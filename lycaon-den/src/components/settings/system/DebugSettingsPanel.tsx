import { SettingsSectionSurface } from "../SettingsSectionSurface.tsx";
import { usePresentationParticipant } from "../../../ui/presentation-context.tsx";
import { SurfaceDeck } from "../../primitives/SurfaceDeck.tsx";
import { For, Show, createEffect, createSignal } from "solid-js";
import {
  CONFIG_DIR_LABEL,
  CONFIG_DIR_LABEL_DEV,
} from "../../../../shared/brand.ts";
import { PreflightProbeList } from "./PreflightProbeList.tsx";
import { SystemInfoPanel } from "./SystemInfoPanel.tsx";
import { ShellCommandPanel } from "./ShellCommandPanel.tsx";
import { DiagnosticsExportPanel } from "./DiagnosticsExportPanel.tsx";
import {
  fullDebugLoggingPref,
  saveFullDebugLogging,
  saveVerboseMode,
  syncDebugPrefsFromSnapshot,
  verboseModePref,
} from "../../../settings/system/debug-prefs.ts";
import { loadSharedAppState } from "../../../store/app-state-snapshot.ts";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { SettingsEditorTitle, AdvancedIcon } from "../SettingsEditorTitle.tsx";
import { UnderlineTabs } from "../UnderlineTabs.tsx";
import { lastSeenSchemaVersion } from "../../../platform/connection/health.ts";
import { DEN_VERSION } from "../../../platform/desktop/den-version.ts";

const debugConfigDirLabel = import.meta.env.DEV
  ? CONFIG_DIR_LABEL_DEV
  : CONFIG_DIR_LABEL;

import { ApprovalsOffPanel } from "../security/ApprovalsOffPanel.tsx";
import { CacheSettingsPanel } from "../storage/CacheSettingsPanel.tsx";
import { DataSettingsPanel } from "../storage/DataSettingsPanel.tsx";
import { BudgetsEditor } from "../budgets/BudgetsEditor.tsx";
import { DeviceTrustPanel } from "../security/DeviceTrustPanel.tsx";
import { HostResourcesSettingsPanel } from "../security/HostResourcesSettingsPanel.tsx";
import type { LycaonClient } from "../../../api/client.ts";
import type { SettingsStore } from "../../../store/settings-store.ts";
import type { CostStore } from "../../../store/cost-store.ts";
import {
  ADVANCED_SETTINGS_TABS,
  DEFAULT_ADVANCED_SETTINGS_TAB,
  type AdvancedSettingsTab,
} from "../../../settings/settings-nav-model.ts";
import { followSettingRevealTab } from "../../../settings/settings-reveal.ts";
import { settingAnchor, settingLabel } from "../../../settings/settings-registry.ts";

type Props = {
  client?: LycaonClient | null;
  settingsStore?: SettingsStore;
  costStore?: CostStore;
  initialTab?: AdvancedSettingsTab;
  /** Opens trust settings for the active project. */
  onOpenProjectTrust?: () => void;
};

export function DebugSettingsPanel(props: Props) {
  const [initialized, setInitialized] = createSignal(false);
  usePresentationParticipant("debug-preferences", () => initialized());
  const [tab, setTab] = createSignal<AdvancedSettingsTab>(
    props.initialTab ?? DEFAULT_ADVANCED_SETTINGS_TAB,
  );

  createEffect(() => {
    const next = props.initialTab;
    if (next) setTab(next);
  });
  followSettingRevealTab("debug", setTab);

  createEffect(() => {
    if (initialized()) return;
    void (async () => {
      await loadSharedAppState();
      syncDebugPrefsFromSnapshot();
      setInitialized(true);
    })();
  });

  const verbose = () => verboseModePref();
  const fullDebug = () => fullDebugLoggingPref();

  return (
    <div class="den-settings-editor" data-testid="debug-settings-panel">
      <SettingsEditorTitle icon={<AdvancedIcon />}>Advanced</SettingsEditorTitle>

      <UnderlineTabs aria-label="Advanced settings sections">
        <For each={ADVANCED_SETTINGS_TABS}>
          {(t) => (
            <button
              type="button"
              role="tab"
              id={`advanced-tab-${t.id}`}
              aria-selected={tab() === t.id}
              aria-controls={`advanced-panel-${t.id}`}
              class="den-underline-tab"
              data-active={tab() === t.id}
              data-testid={`advanced-tab-${t.id}`}
              onClick={() => setTab(t.id)}
            >
              {t.label}
            </button>
          )}
        </For>
      </UnderlineTabs>

      <SurfaceDeck active={tab()}>
        {(displayedTab) => <>
      <Show when={displayedTab === "budgets"}>
        <div
          id="advanced-panel-budgets"
          role="tabpanel"
          aria-labelledby="advanced-tab-budgets"
          data-testid="advanced-panel-budgets"
        >
          <Show
            when={props.client}
            keyed
            fallback={
              <p class="den-settings-hint" data-testid="budgets-offline">
                Budgets settings require a connected backend.
              </p>
            }
          >
            {(c) => {
              const store = props.settingsStore;
              if (!store) {
                return (
                  <p class="den-settings-hint" data-testid="budgets-offline">
                    Budgets settings require a connected backend.
                  </p>
                );
              }
              return (
                <SettingsSectionSurface panel="budgets" client={c} store={store} projects={[]}>
                <BudgetsEditor
                  client={c}
                  settingsStore={store}
                  costStore={props.costStore}
                  embedded
                />
                </SettingsSectionSurface>
              );
            }}
          </Show>
        </div>
      </Show>

      <Show when={displayedTab === "approvals"}>
        <div
          id="advanced-panel-approvals"
          role="tabpanel"
          aria-labelledby="advanced-tab-approvals"
          data-testid="advanced-panel-approvals"
        >
          <ApprovalsOffPanel client={props.client} />
        </div>
      </Show>

      <Show when={displayedTab === "host_resources"}>
        <div
          id="advanced-panel-host_resources"
          role="tabpanel"
          aria-labelledby="advanced-tab-host_resources"
          data-testid="advanced-panel-host_resources"
        >
          <Show
            when={props.client}
            keyed
            fallback={
              <p class="den-settings-hint" data-testid="host-resources-offline">
                Host resources require a connected backend.
              </p>
            }
          >
            {(client) => <HostResourcesSettingsPanel client={client} embedded />}
          </Show>
        </div>
      </Show>

      <Show when={displayedTab === "cache"}>
        <div
          id="advanced-panel-cache"
          role="tabpanel"
          aria-labelledby="advanced-tab-cache"
          data-testid="advanced-panel-cache"
        >
          <CacheSettingsPanel client={props.client} />
        </div>
      </Show>

      <Show when={displayedTab === "data"}>
        <div
          id="advanced-panel-data"
          role="tabpanel"
          aria-labelledby="advanced-tab-data"
          data-testid="advanced-panel-data"
        >
          <DataSettingsPanel client={props.client} />
        </div>
      </Show>

      <Show when={displayedTab === "diagnostics"}>
        <div
          id="advanced-panel-diagnostics"
          role="tabpanel"
          aria-labelledby="advanced-tab-diagnostics"
          data-testid="advanced-panel-diagnostics"
          class="den-settings-section"
        >
          <div class="den-settings-pref-group">
            <div class="den-settings-pref-row" {...settingAnchor("verbose-mode")}>
              <div class="den-settings-pref-copy">
                <span class="den-settings-pref-label">{settingLabel("verbose-mode")}</span>
                <p class="den-settings-hint">
                  Show expected recoverable tool rejects (coordinator scope guards,
                  informational feedback banners), and background index prefetch
                  rows. Workflow gates and approval blocks are always visible.
                </p>
              </div>
              <DenCheckbox
                checked={verbose()}
                disabled={!initialized()}
                data-testid="debug-verbose-mode"
                onChange={(e) => {
                  void saveVerboseMode(e.currentTarget.checked);
                }}
              >
                <span class="sr-only">{settingLabel("verbose-mode")}</span>
              </DenCheckbox>
            </div>
            <div class="den-settings-pref-row" {...settingAnchor("debug-logging")}>
              <div class="den-settings-pref-copy">
                <span class="den-settings-pref-label">{settingLabel("debug-logging")}</span>
                <p class="den-settings-hint">
                  Capture verbose backend logs plus LLM, HTTP, SSE, tool, and Den
                  main-thread stall traces (<code>den-perf.jsonl</code>) to
                  <code> {debugConfigDirLabel}/ </code> for diagnostics. Takes effect
                  after the app restarts. Traces may contain prompt and response
                  content — leave this off unless you are investigating an issue.
                </p>
              </div>
              <DenCheckbox
                checked={fullDebug()}
                disabled={!initialized()}
                data-testid="debug-full-logging"
                onChange={(e) => {
                  void saveFullDebugLogging(e.currentTarget.checked);
                }}
              >
                <span class="sr-only">{settingLabel("debug-logging")}</span>
              </DenCheckbox>
            </div>
          </div>

          <SystemInfoPanel />

          <ShellCommandPanel />

          <PreflightProbeList />

          <DiagnosticsExportPanel />

          <div class="den-settings-subsection">
            <h3 class="den-settings-subhead">Backend</h3>
            <p class="den-settings-hint" data-testid="debug-schema-version">
              Den {DEN_VERSION}
              {lastSeenSchemaVersion() !== null
                ? ` · store schema_version ${lastSeenSchemaVersion()}`
                : " · store schema_version (not connected)"}
            </p>
          </div>
        </div>
      </Show>

      <Show when={displayedTab === "project_trust"}>
        <div
          id="advanced-panel-project_trust"
          role="tabpanel"
          aria-labelledby="advanced-tab-project_trust"
          data-testid="advanced-panel-project-trust"
        >
          <Show
            when={props.client}
            keyed
            fallback={
              <p class="den-settings-hint" data-testid="project-trust-offline">
                Project trust settings require a connected backend.
              </p>
            }
          >
            {(client) => (
              <DeviceTrustPanel
                client={client}
                onOpenProjectTrust={props.onOpenProjectTrust}
              />
            )}
          </Show>
        </div>
      </Show>
        </>}
      </SurfaceDeck>
    </div>
  );
}
