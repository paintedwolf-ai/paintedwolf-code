import { Show, createEffect, onCleanup, untrack } from "solid-js";
import { lastSeenHostVersion } from "../../platform/connection/health.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { SettingsStore } from "../../store/settings-store.ts";
import type { CostStore } from "../../store/cost-store.ts";
import type { createProjectsStore } from "../../store/projects-store.ts";
import {
  useSettingsBackend,
} from "../../settings/settings-backend.ts";
import {
  type AdvancedSettingsTab,
  type GeneralSettingsTab,
  type SettingsSection,
} from "../../settings/settings-nav-model.ts";
import {
  DEVICE_TO_MIRROR_SECTION,
  PROJECT_SETTINGS_OVERLAY_COPY,
  type DeviceSettingsCounterpart,
  type ProjectSettingsMirrorSection,
} from "../../settings/security/project-settings-overlay-copy.ts";
import { DebugSettingsPanel } from "./system/DebugSettingsPanel.tsx";
import { GeneralSettingsPanel } from "./appearance/GeneralSettingsPanel.tsx";
import { ExtensionsSettingsPanel } from "./extensions/ExtensionsSettingsPanel.tsx";
import { McpSettingsPanel } from "./mcp/McpSettingsPanel.tsx";
import { WebResearchSettingsPanel } from "./providers/WebResearchSettingsPanel.tsx";
import { ProvidersSettingsPanel } from "./providers/ProvidersSettingsPanel.tsx";
import { ApprovalsPanel } from "./security/ApprovalsPanel.tsx";
import { ScannersSettingsPanel } from "./security/ScannersSettingsPanel.tsx";
import { CostSourcesPanel } from "./budgets/CostSourcesPanel.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { SurfaceDeck } from "../primitives/SurfaceDeck.tsx";
import { SettingsSectionSurface } from "./SettingsSectionSurface.tsx";
import { SettingsStagePanel } from "./SettingsStagePanel.tsx";
import {
  pendingSettingReveal,
  revealSettingAnchor,
  settleSettingReveal,
} from "../../settings/settings-reveal.ts";

type ProjectsStore = ReturnType<typeof createProjectsStore>;

type Props = {
  section: SettingsSection;
  /** Deep-link into a General tab (e.g. Keyboard from the help overlay). */
  generalInitialTab?: GeneralSettingsTab;
  /** Deep-link into an Advanced tab. */
  advancedInitialTab?: AdvancedSettingsTab;
  appStore: AppStore;
  settingsStore: SettingsStore;
  costStore?: CostStore;
  projects: ProjectsStore;
  projectDir?: string;
  /** Active project display name for the Customize CTA. */
  projectName?: string;
  onOpenScanners?: () => void;
  onOpenProjectSettings?: (section: ProjectSettingsMirrorSection) => void;
  /** Opens trust settings for the active project. */
  onOpenProjectTrust?: () => void;
  onSecurityScannersFeatureChange?: (enabled: boolean) => void;
  /** Exit Settings (same as Escape). */
  onClose?: () => void;
  /** Set when a routed jump (card CTA, Crossbar go-to) landed here. */
  back?: import("../shell/StageBackChip.tsx").StageBack | null;
};

function deviceCounterpart(
  section: DeviceSettingsCounterpart,
  opts: {
    projectName?: string;
    onOpen?: (section: ProjectSettingsMirrorSection) => void;
  },
): { label: string; onOpen: () => void } | undefined {
  if (!opts.onOpen) return undefined;
  const mirror = DEVICE_TO_MIRROR_SECTION[section];
  const path = PROJECT_SETTINGS_OVERLAY_COPY.settingsPath[mirror];
  const label = opts.projectName
    ? PROJECT_SETTINGS_OVERLAY_COPY.openInProjectSettingsNamed(
        opts.projectName,
        path,
      )
    : PROJECT_SETTINGS_OVERLAY_COPY.openInProjectSettings(path);
  return {
    label,
    onOpen: () => opts.onOpen?.(mirror),
  };
}

export function SettingsView(props: Props) {
  const { client } = useSettingsBackend(props.appStore);
  let root!: HTMLElement;

  // A pending setting reveal lands once its section is the one shown here.
  createEffect(() => {
    const request = pendingSettingReveal();
    if (!request || request.setting.section !== props.section) return;
    let disposed = false;
    onCleanup(() => {
      disposed = true;
    });
    const isCurrent = () =>
      !disposed &&
      untrack(pendingSettingReveal) === request &&
      untrack(() => props.section) === request.setting.section;
    void revealSettingAnchor(root, request.setting.id, {
      timeoutMs: request.expiresAt - Date.now(),
      isCurrent,
    }).finally(() => settleSettingReveal(request));
  });

  // Keep stage chrome mounted across section changes.
  return (
    <Scrollport
      ref={(el) => (root = el)}
      class="den-settings-view"
      contentAs="section"
      data-testid="settings-view"
    >
      <div class="den-settings-view-body">
        <SettingsStagePanel
          appStore={props.appStore}
          back={props.back}
          error={props.settingsStore.state.error}
          errorTestId="settings-error"
          onClose={props.onClose}
          closeLabel="Close settings"
          offline={
            <SurfaceDeck active={props.section}>
              {(section) => (
                <SettingsSectionSurface
                  panel={section === "providers" || section === "approvals" ? section : section === "cost" ? "pricing" : undefined}
                  client={client()}
                  store={props.settingsStore}
                  projects={props.projects.state.projects}
                >
                  <Show when={section === "general"}>
                    <GeneralSettingsPanel initialTab={props.generalInitialTab} version={lastSeenHostVersion()} client={client()} />
                  </Show>
                  <Show when={section === "debug"}>
                    <DebugSettingsPanel client={client()} settingsStore={props.settingsStore} costStore={props.costStore}
                      initialTab={props.advancedInitialTab} onOpenProjectTrust={props.onOpenProjectTrust} />
                  </Show>
                  <Show when={client()} keyed>
                    {(c: LycaonClient) => {
                const providersJump = deviceCounterpart("providers", {
                  projectName: props.projectName,
                  onOpen: props.onOpenProjectSettings,
                });
                const approvalsJump = deviceCounterpart("approvals", {
                  projectName: props.projectName,
                  onOpen: props.onOpenProjectSettings,
                });
                const mcpJump = deviceCounterpart("mcp", {
                  projectName: props.projectName,
                  onOpen: props.onOpenProjectSettings,
                });
                const scannersJump = deviceCounterpart("scanners", {
                  projectName: props.projectName,
                  onOpen: props.onOpenProjectSettings,
                });
                      return (

                <div>
                  <Show when={section === "providers"}>
                    <ProvidersSettingsPanel
                      client={c}
                      settingsStore={props.settingsStore}
                      counterpartLabel={providersJump?.label}
                      onOpenCounterpart={providersJump?.onOpen}
                    />
                  </Show>
                  <Show when={section === "mcp"}>
                    <McpSettingsPanel
                      client={c}
                      counterpartLabel={mcpJump?.label}
                      onOpenCounterpart={mcpJump?.onOpen}
                    />
                  </Show>
                  <Show when={section === "scanners"}>
                    <ScannersSettingsPanel
                      client={c}
                      counterpartLabel={scannersJump?.label}
                      onOpenCounterpart={scannersJump?.onOpen}
                      onEnabledChange={props.onSecurityScannersFeatureChange}
                    />
                  </Show>
                  <Show when={section === "web-research"}>
                    <WebResearchSettingsPanel client={c} />
                  </Show>
                  <Show when={section === "extensions"}>
                    <ExtensionsSettingsPanel
                      client={c}
                      surface="settings"
                      extensionsRevision={() => props.appStore.state.extensionsRevision}
                      onOpenScanners={props.onOpenScanners}
                    />
                  </Show>
                  <Show when={section === "approvals"}>
                    <ApprovalsPanel
                      client={c}
                      settingsStore={props.settingsStore}
                      counterpartLabel={approvalsJump?.label}
                      onOpenCounterpart={approvalsJump?.onOpen}
                    />
                  </Show>
                  <Show when={section === "cost"}>
                    <CostSourcesPanel
                      client={c}
                      settingsStore={props.settingsStore}
                    />
                  </Show>
                </div>
                      );
                    }}
                  </Show>
                </SettingsSectionSurface>
              )}
            </SurfaceDeck>
          }
        />
      </div>
    </Scrollport>
  );
}
