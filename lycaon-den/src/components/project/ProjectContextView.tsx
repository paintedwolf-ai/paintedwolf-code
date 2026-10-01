import { createMemo, untrack, For, Show } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { createSettingsStore, type SettingsStore } from "../../store/settings-store.ts";
import type { createProjectsStore } from "../../store/projects-store.ts";
import {
  isSettingsStorePanel,
} from "../../settings/settings-actions.ts";
import {
  PROJECT_CONTEXT,
  type ProjectContextSection,
  isProjectContextSection,
} from "../../settings/settings-nav-model.ts";
import {
  PROJECT_SETTINGS_OVERLAY_COPY,
  isProjectSettingsMirrorSection,
  type DeviceSettingsCounterpart,
  type ProjectSettingsMirrorSection,
  MIRROR_TO_DEVICE_SECTION,
} from "../../settings/security/project-settings-overlay-copy.ts";
import { TRUST_COPY } from "../../settings/security/trust-copy.ts";
import { EditReviewSettingsPanel } from "../settings/appearance/EditReviewSettingsPanel.tsx";
import { McpSettingsPanel } from "../settings/mcp/McpSettingsPanel.tsx";
import { ScannersSettingsPanel } from "../settings/security/ScannersSettingsPanel.tsx";
import { ApprovalsPanel } from "../settings/security/ApprovalsPanel.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { SurfaceDeck } from "../primitives/SurfaceDeck.tsx";
import { SettingsSectionSurface } from "../settings/SettingsSectionSurface.tsx";
import { SettingsStagePanel } from "../settings/SettingsStagePanel.tsx";
import { UnderlineTabs } from "../settings/UnderlineTabs.tsx";
import { ModelsEditor } from "../model/ModelsEditor.tsx";
import { VerifyEditor } from "../../files/editor/VerifyEditor.tsx";
import { ProjectTrustPanel } from "../trust/ProjectTrustPanel.tsx";
import { ProjectSecretsPanel } from "./ProjectSecretsPanel.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { tauriDragRegionProps } from "../../platform/runtime.ts";

type ProjectsStore = ReturnType<typeof createProjectsStore>;

type Props = {
  section: ProjectContextSection;
  onSectionChange: (section: ProjectContextSection) => void;
  projectId: string;
  sessionId?: string;
  projectDir?: string;
  appStore: AppStore;
  settingsStore: SettingsStore;
  projects: ProjectsStore;
  /** Opens device-wide project trust settings. */
  onOpenDeviceTrust?: () => void;
  onOpenExtensionsSettings?: () => void;
  /** Open the matching device Settings section. */
  onOpenDeviceSettings?: (section: DeviceSettingsCounterpart) => void;
  /** Exit Project configuration (same as Escape). */
  onClose?: () => void;
  /** Set when a routed jump (card CTA, Crossbar go-to) landed here. */
  back?: import("../shell/StageBackChip.tsx").StageBack | null;
};

function counterpartFor(
  section: ProjectSettingsMirrorSection,
  onOpen?: (section: DeviceSettingsCounterpart) => void,
): { label: string; onOpen: () => void } | undefined {
  if (!onOpen) return undefined;
  const device = MIRROR_TO_DEVICE_SECTION[section];
  const path = PROJECT_SETTINGS_OVERLAY_COPY.deviceSettingsPath[section];
  return {
    label: PROJECT_SETTINGS_OVERLAY_COPY.openInSettings(path),
    onOpen: () => onOpen(device),
  };
}

export function ProjectContextView(props: Props) {

  const settingsStore = createMemo(() => {
    props.projectId;
    const device = props.settingsStore;
    return untrack(() => createSettingsStore(undefined, device));
  });

  const editorProps = () => ({
    alwaysProjectScope: true as const,
    appStore: props.appStore,
    settingsStore: settingsStore(),
    projects: props.projects,
    projectDir: props.projectDir,
  });

  const header = (
    <header
      class="project-context-view__header"
      data-testid="project-configuration-header"
      {...chromeProps()}
      // Empty titlebar space remains draggable.
      {...tauriDragRegionProps()}
    >
      <h1 class="project-context-view__title">
        {PROJECT_SETTINGS_OVERLAY_COPY.stageTitle}
      </h1>
      <UnderlineTabs aria-label="Project configuration sections">
        <For each={PROJECT_CONTEXT}>
          {(t) => (
            <button
              type="button"
              role="tab"
              id={`project-config-tab-${t.id}`}
              aria-selected={props.section === t.id}
              aria-controls={`project-config-panel-${t.id}`}
              class="den-underline-tab"
              data-active={props.section === t.id}
              data-testid={`project-context-entry-${t.id}`}
              onClick={() => props.onSectionChange(t.id)}
            >
              {t.label}
            </button>
          )}
        </For>
      </UnderlineTabs>
    </header>
  );

  return (
    <Scrollport
      class="project-context-view den-settings-view"
      contentAs="section"
      data-testid="project-context-view"
    >
      <div class="den-settings-view-body">
        {/* Keep stage chrome mounted across tab changes. */}
        <SettingsStagePanel
          appStore={props.appStore}
          back={props.back}
          error={settingsStore().state.error}
          errorTestId="project-context-error"
          before={header}
          onClose={props.onClose}
          closeLabel="Close project configuration"
        >
          {(c: LycaonClient) => (
            <SurfaceDeck active={props.section}>
              {(section) => {
                const mirror = isProjectSettingsMirrorSection(section) ? section : null;
                const jump = mirror
                  ? counterpartFor(mirror, props.onOpenDeviceSettings)
                  : undefined;
                return (
                  <SettingsSectionSurface panel={isProjectContextSection(section) && isSettingsStorePanel(section) ? section : undefined}
                    client={c} store={settingsStore()} projects={props.projects.state.projects} projectDir={props.projectDir} projectScope>
                  <div
                    role="tabpanel"
                    class="den-settings-section"
                    id={`project-config-panel-${section}`}
                    aria-labelledby={`project-config-tab-${section}`}
                  >
                    <Show when={section === "trust"}>
                      <ProjectTrustPanel
                        client={c}
                        projectId={props.projectId}
                        projectRoots={props.projects.byId(props.projectId)?.roots ?? []}
                        counterpartLabel={
                          props.onOpenDeviceTrust
                            ? PROJECT_SETTINGS_OVERLAY_COPY.openInSettings(
                                `Advanced → ${TRUST_COPY.sectionTitle}`,
                              )
                            : undefined
                        }
                        onOpenCounterpart={props.onOpenDeviceTrust}
                        onOpenExtensions={props.onOpenExtensionsSettings}
                      />
                    </Show>
                    <Show when={section === "secrets"}>
                      <ProjectSecretsPanel client={c} projectId={props.projectId} />
                    </Show>
                    <Show when={section === "providers"}>
                      <ModelsEditor
                        client={c}
                        appStore={props.appStore}
                        settingsStore={settingsStore()}
                        projects={props.projects}
                        projectDir={props.projectDir}
                        counterpartLabel={jump?.label}
                        onOpenCounterpart={jump?.onOpen}
                      />
                    </Show>
                    <Show when={section === "approvals"}>
                      <ApprovalsPanel
                        client={c}
                        settingsStore={settingsStore()}
                        projectId={props.projectId}
                        sessionId={props.sessionId}
                        alwaysProjectScope
                        counterpartLabel={jump?.label}
                        onOpenCounterpart={jump?.onOpen}
                      />
                    </Show>
                    <Show when={section === "mcp"}>
                      <McpSettingsPanel
                        client={c}
                        projectId={props.projectId}
                        alwaysProjectScope
                        counterpartLabel={jump?.label}
                        onOpenCounterpart={jump?.onOpen}
                      />
                    </Show>
                    <Show when={section === "scanners"}>
                      <ScannersSettingsPanel
                        client={c}
                        projectId={props.projectId}
                        alwaysProjectScope
                        counterpartLabel={jump?.label}
                        onOpenCounterpart={jump?.onOpen}
                      />
                    </Show>
                    <Show when={section === "edit-review"}>
                      <EditReviewSettingsPanel client={c} {...editorProps()} />
                    </Show>
                    <Show when={section === "tests"}>
                      <VerifyEditor
                        client={c}
                        appStore={props.appStore}
                        projectId={props.projectId}
                        projectDir={props.projectDir}
                      />
                    </Show>
                  </div>
                  </SettingsSectionSurface>
                );
              }}
            </SurfaceDeck>
          )}
        </SettingsStagePanel>
      </div>
    </Scrollport>
  );
}
