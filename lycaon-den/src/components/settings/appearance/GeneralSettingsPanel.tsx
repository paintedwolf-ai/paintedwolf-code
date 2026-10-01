import { usePresentationParticipant } from "../../../ui/presentation-context.tsx";
import { SurfaceDeck } from "../../primitives/SurfaceDeck.tsx";
import { AppearanceSettings } from "./AppearanceSettings.tsx";
import { TypographySettings } from "./TypographySettings.tsx";
import { SettingsGovernedGroup } from "../SettingsGovernedGroup.tsx";
import { For, Show, createEffect, createSignal } from "solid-js";
import {
  diffCollapsedPref,
  diffWordWrapPref,
  saveDiffCollapsed,
  saveDiffWordWrap,
  saveShowAllModels,
  showAllModelsPref,
  syncDisplayPrefsFromSnapshot,
} from "../../../settings/appearance/display-prefs.ts";
import {
  messageTimesPref,
  saveMessageTimes,
  syncChatPrefsFromSnapshot,
} from "../../../settings/chat/chat-prefs.ts";
import {
  narrowSurvivorFollowsLaunchPref,
  narrowSurvivorPref,
  saveNarrowSurvivor,
  saveStartupCompanion,
  saveWorkspaceOrientation,
  saveSplitOrder,
  splitOrderPref,
  startupCompanionPref,
  syncLayoutFromSnapshot,
  workspaceOrientationPref,
} from "../../../shell/layout-store.ts";
import { CONTEXT_NAV_CATALOG } from "../../../../shared/app-state-types.ts";
import { isContextNavItemId } from "../../../settings/editor/context-nav-prefs.ts";
import { stageLabelFor } from "../../stage/stage-registry.tsx";
import {
  recordNotificationPermission,
  notificationFinishedPref,
  notificationNeedsApprovalPref,
  notificationNeedsYouPref,
  notificationPermissionResult,
  notificationsEnabledPref,
  saveNotificationFinished,
  saveNotificationNeedsApproval,
  saveNotificationNeedsYou,
  saveNotificationsEnabled,
  syncNotificationPrefsFromSnapshot,
} from "../../../settings/chat/notification-prefs.ts";
import { MODELS_ROLE_FILTER_COPY } from "../../../settings/providers/models-role-filter-copy.ts";
import {
  firstTimeTipsEnabledPref,
  saveFirstTimeTipsEnabled,
  syncFirstTimeTipsFromSnapshot,
} from "../../../settings/system/first-time-tips-prefs.ts";
import { loadSharedAppState } from "../../../store/app-state-snapshot.ts";
import { BrowseSegmented } from "../../browse/BrowseSegmented.tsx";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { DenSelect } from "../../primitives/DenSelect.tsx";
import { SettingsEditorTitle, GeneralIcon } from "../SettingsEditorTitle.tsx";
import { chromeProps } from "../../../styling/ui-chrome.ts";
import { UnderlineTabs } from "../UnderlineTabs.tsx";
import {
  ensureNotificationPermission,
  sendNotification,
} from "../../../platform/desktop/notifications.ts";
import { NOTIFICATION_PRODUCT_TITLE } from "../../../notifications/notification-service.ts";
import { EditorSettingsPanel } from "./EditorSettingsPanel.tsx";
import { KeyboardSettingsPanel } from "./KeyboardSettingsPanel.tsx";
import { UpdatesSettingsPanel } from "../system/UpdatesSettingsPanel.tsx";
import type { UpdatePanelProps } from "../../update/UpdatePanel.tsx";
import { PowerSettingsPanel } from "../system/PowerSettingsPanel.tsx";
import type { LycaonClient } from "../../../api/client.ts";
import {
  fileSummariesEnabled,
  fileSummariesSettingKnown,
  refreshFileSummariesSetting,
  saveFileSummariesEnabled,
} from "../../../settings/editor/file-summary-settings.ts";
import { AboutSettingsPanel } from "../system/AboutSettingsPanel.tsx";
import {
  DEFAULT_GENERAL_SETTINGS_TAB,
  GENERAL_SETTINGS_TABS,
  type GeneralSettingsTab,
} from "../../../settings/settings-nav-model.ts";
import { followSettingRevealTab } from "../../../settings/settings-reveal.ts";
import { settingAnchor, settingLabel } from "../../../settings/settings-registry.ts";

type Props = {
  initialTab?: GeneralSettingsTab;
  version: string | null;
  updateService?: UpdatePanelProps["updateService"];
  client?: LycaonClient | null;
};

export function GeneralSettingsPanel(props: Props) {
  const [initialized, setInitialized] = createSignal(false);
  usePresentationParticipant("display-preferences", () => initialized());
  const [fileSummariesError, setFileSummariesError] = createSignal<string | null>(null);
  usePresentationParticipant("file-summary-setting", () => !props.client || fileSummariesSettingKnown() || fileSummariesError() != null);
  const [tab, setTab] = createSignal<GeneralSettingsTab>(
    props.initialTab ?? DEFAULT_GENERAL_SETTINGS_TAB,
  );

  createEffect(() => {
    const next = props.initialTab;
    if (next) setTab(next);
  });
  followSettingRevealTab("general", setTab);

  createEffect(() => {
    const client = props.client;
    if (!client) return;
    void refreshFileSummariesSetting(client).catch((error) => {
      setFileSummariesError(error instanceof Error ? error.message : String(error));
    });
  });

  // Accessors avoid creating reactive computations in click handlers.
  const narrowSurvivorValue = () =>
    narrowSurvivorFollowsLaunchPref() ? "" : narrowSurvivorPref();
  const narrowSurvivorOptions = () => [
    {
      value: "",
      label: `Match open at launch (${
        startupCompanionPref() ? "context view" : "conversation"
      })`,
    },
    { value: "conversation", label: "Keep the conversation" },
    { value: "stage", label: "Keep the context view" },
  ];

  createEffect(() => {
    if (initialized()) return;
    void (async () => {
    await loadSharedAppState();
    syncDisplayPrefsFromSnapshot();
    syncChatPrefsFromSnapshot();
    syncNotificationPrefsFromSnapshot();
    syncFirstTimeTipsFromSnapshot();
    syncLayoutFromSnapshot();
    setInitialized(true);
    })();
  });

  return (
    <div class="den-settings-editor" data-testid="general-settings-panel">
      <SettingsEditorTitle icon={<GeneralIcon />}>General</SettingsEditorTitle>

      <UnderlineTabs aria-label="General settings sections">
        <For each={GENERAL_SETTINGS_TABS}>
          {(t) => (
            <button
              type="button"
              role="tab"
              id={`general-tab-${t.id}`}
              aria-selected={tab() === t.id}
              aria-controls={`general-panel-${t.id}`}
              class="den-underline-tab"
              data-active={tab() === t.id}
              data-testid={`general-tab-${t.id}`}
              onClick={() => setTab(t.id)}
            >
              {t.label}
            </button>
          )}
        </For>
      </UnderlineTabs>

      <SurfaceDeck active={tab()}>
        {(displayedTab) => <>
      <Show when={displayedTab === "display"}>
        <div
          id="general-panel-display"
          role="tabpanel"
          aria-labelledby="general-tab-display"
          data-testid="general-panel-display"
        >
          <AppearanceSettings />
          <TypographySettings />
          <div class="den-settings-pref-group">
            <div class="den-settings-pref-row" {...settingAnchor("message-times")}>
              <div class="den-settings-pref-copy">
                <span class="den-settings-pref-label">{settingLabel("message-times")}</span>
                <p class="den-settings-hint">
                  Chats always show the day, a time after a quiet hour, and when
                  each turn finished. Choose when your own messages show the
                  time they were sent.
                </p>
              </div>
              <BrowseSegmented
                class="den-settings-segmented"
                testId="display-message-times"
                ariaLabel={settingLabel("message-times")}
                disabled={!initialized()}
                value={messageTimesPref()}
                onChange={(id) => {
                  if (id === "hover" || id === "always") void saveMessageTimes(id);
                }}
                options={[
                  { id: "hover", label: "On hover", testId: "display-message-times-hover" },
                  { id: "always", label: "Always", testId: "display-message-times-always" },
                ]}
              />
            </div>
            <div class="den-settings-pref-row" {...settingAnchor("collapse-diffs")}>
              <div class="den-settings-pref-copy">
                <span class="den-settings-pref-label">{settingLabel("collapse-diffs")}</span>
                <p class="den-settings-hint">
                  When files are edited, the diff preview will start collapsed by
                  default.
                </p>
              </div>
              <DenCheckbox
                checked={diffCollapsedPref()}
                disabled={!initialized()}
                data-testid="display-diff-collapsed"
                onChange={(e) => {
                  void saveDiffCollapsed(e.currentTarget.checked);
                }}
              >
                <span class="sr-only">{settingLabel("collapse-diffs")}</span>
              </DenCheckbox>
            </div>
            <div class="den-settings-pref-row" {...settingAnchor("file-summaries")}>
              <div class="den-settings-pref-copy">
                <span class="den-settings-pref-label">{settingLabel("file-summaries")}</span>
                <p class="den-settings-hint">
                  Show AI-generated summaries in the file editor. Turning this off
                  stops summary work and deletes stored summaries.
                </p>
                <Show when={fileSummariesError()}>
                  {(message) => <p class="den-settings-warn" role="alert">{message()}</p>}
                </Show>
              </div>
              <DenCheckbox
                checked={fileSummariesEnabled()}
                disabled={!props.client || !fileSummariesSettingKnown()}
                data-testid="display-file-summaries"
                onChange={(e) => {
                  const client = props.client;
                  if (!client) return;
                  setFileSummariesError(null);
                  void saveFileSummariesEnabled(client, e.currentTarget.checked).catch(
                    (error) => {
                      setFileSummariesError(
                        error instanceof Error ? error.message : String(error),
                      );
                    },
                  );
                }}
              >
                <span class="sr-only">{settingLabel("file-summaries")}</span>
              </DenCheckbox>
            </div>
            <div class="den-settings-pref-row" {...settingAnchor("diff-word-wrap")}>
              <div class="den-settings-pref-copy">
                <span class="den-settings-pref-label">{settingLabel("diff-word-wrap")}</span>
                <p class="den-settings-hint">
                  In the diff view, wrap long lines in inline file edit previews so
                  they fit the chat column.
                </p>
              </div>
              <DenCheckbox
                checked={diffWordWrapPref()}
                disabled={!initialized()}
                data-testid="display-diff-word-wrap"
                onChange={(e) => {
                  void saveDiffWordWrap(e.currentTarget.checked);
                }}
              >
                <span class="sr-only">{settingLabel("diff-word-wrap")}</span>
              </DenCheckbox>
            </div>
            <div class="den-settings-pref-row" {...settingAnchor("show-all-models")}>
              <div class="den-settings-pref-copy">
                <span class="den-settings-pref-label">
                  {settingLabel("show-all-models")}
                </span>
                <p class="den-settings-hint">
                  {MODELS_ROLE_FILTER_COPY.showAllModelsSubcopy}
                </p>
              </div>
              <DenCheckbox
                checked={showAllModelsPref()}
                disabled={!initialized()}
                data-testid="display-show-all-models"
                onChange={(e) => {
                  void saveShowAllModels(e.currentTarget.checked);
                }}
              >
                <span class="sr-only">
                  {settingLabel("show-all-models")}
                </span>
              </DenCheckbox>
            </div>
            <div class="den-settings-pref-row" {...settingAnchor("first-time-tips")}>
              <div class="den-settings-pref-copy">
                <span class="den-settings-pref-label">{settingLabel("first-time-tips")}</span>
                <p class="den-settings-hint">
                  Show short explanations when you first encounter an interface.
                </p>
              </div>
              <DenCheckbox
                checked={firstTimeTipsEnabledPref()}
                disabled={!initialized()}
                data-testid="display-first-time-tips"
                onChange={(e) => {
                  void saveFirstTimeTipsEnabled(e.currentTarget.checked);
                }}
              >
                <span class="sr-only">{settingLabel("first-time-tips")}</span>
              </DenCheckbox>
            </div>
          </div>

          <div class="den-settings-pref-group" data-testid="display-layout-group">
            <h3 class="den-settings-pref-group-title" {...chromeProps()}>Layout</h3>
            <div class="den-settings-pref-row" {...settingAnchor("open-at-launch")}>
              <div class="den-settings-pref-copy">
                <span class="den-settings-pref-label">{settingLabel("open-at-launch")}</span>
                <p class="den-settings-hint">
                  Chat only restores the last layout. Split view always opens a
                  context surface beside the conversation — Files, unless you
                  pick another. Choose a context view here to start with that
                  view every launch.
                </p>
              </div>
              <DenSelect
                aria-label={settingLabel("open-at-launch")}
                disabled={!initialized()}
                data-testid="display-startup-companion"
                value={startupCompanionPref() ?? ""}
                options={[
                  { value: "", label: "Chat only" },
                  ...CONTEXT_NAV_CATALOG.map((stageId) => ({
                    value: stageId,
                    label: `Split with ${stageLabelFor(stageId)}`,
                  })),
                ]}
                onValueChange={(next) => {
                  void saveStartupCompanion(
                    isContextNavItemId(next) ? next : null,
                  );
                }}
              />
            </div>
            <div class="den-settings-pref-row" {...settingAnchor("narrow-window")}>
              <div class="den-settings-pref-copy">
                <span class="den-settings-pref-label">
                  {settingLabel("narrow-window")}
                </span>
                <p class="den-settings-hint">
                  A split needs room for both columns. Under that width it shows
                  one of them, and the other returns when the window has space
                  again. Matching open at launch keeps the conversation, or the
                  context view when you launch into a split.
                </p>
              </div>
              <DenSelect
                aria-label={settingLabel("narrow-window")}
                disabled={!initialized()}
                data-testid="display-narrow-survivor"
                value={narrowSurvivorValue()}
                options={narrowSurvivorOptions()}
                onValueChange={(next) => {
                  void saveNarrowSurvivor(
                    next === "conversation" || next === "stage" ? next : null,
                  );
                }}
              />
            </div>
            <div class="den-settings-pref-row" {...settingAnchor("split-order")}>
              <div class="den-settings-pref-copy">
                <span class="den-settings-pref-label">{settingLabel("split-order")}</span>
                <p class="den-settings-hint">
                  Place chat beside the sidebar or beyond context in split view.
                  Mirroring reverses the whole arrangement.
                </p>
              </div>
              <BrowseSegmented
                class="den-settings-segmented"
                testId="display-split-order"
                ariaLabel={settingLabel("split-order")}
                disabled={!initialized()}
                value={splitOrderPref()}
                onChange={(id) => {
                  if (id === "context-first" || id === "chat-first") void saveSplitOrder(id);
                }}
                options={[
                  { id: "context-first", label: "Beyond context", testId: "display-context-first" },
                  { id: "chat-first", label: "Beside sidebar", testId: "display-chat-first" },
                ]}
              />
            </div>
            <div class="den-settings-pref-row" {...settingAnchor("workspace-orientation")}>
              <div class="den-settings-pref-copy">
                <span class="den-settings-pref-label">{settingLabel("workspace-orientation")}</span>
                <p class="den-settings-hint">
                  Standard keeps navigation on the left. Mirrored reverses the
                  whole workspace while preserving reading order.
                </p>
              </div>
              <BrowseSegmented
                class="den-settings-segmented"
                testId="display-workspace-orientation"
                ariaLabel={settingLabel("workspace-orientation")}
                disabled={!initialized()}
                value={workspaceOrientationPref()}
                onChange={(id) => {
                  if (id === "standard" || id === "mirrored") {
                    void saveWorkspaceOrientation(id);
                  }
                }}
                options={[
                  {
                    id: "standard",
                    label: "Standard",
                    testId: "display-workspace-standard",
                  },
                  {
                    id: "mirrored",
                    label: "Mirrored",
                    testId: "display-workspace-mirrored",
                  },
                ]}
              />
            </div>
          </div>
        </div>
      </Show>

      <Show when={displayedTab === "notifications"}>
        <div
          id="general-panel-notifications"
          role="tabpanel"
          aria-labelledby="general-tab-notifications"
          data-testid="general-panel-notifications"
        >
          <div class="den-settings-pref-group">
            <div class="den-settings-pref-row" {...settingAnchor("notifications")}>
              <div class="den-settings-pref-copy">
                <span class="den-settings-pref-label">{settingLabel("notifications")}</span>
                <p class="den-settings-hint">
                  Show a native notification when a run finishes or the agent
                  needs you, while the window is in the background.
                </p>
              </div>
              <DenCheckbox
                checked={notificationsEnabledPref()}
                disabled={!initialized()}
                data-testid="notifications-enabled"
                onChange={(e) => {
                  void saveNotificationsEnabled(e.currentTarget.checked);
                }}
              >
                <span class="sr-only">{settingLabel("notifications")}</span>
              </DenCheckbox>
            </div>
            <SettingsGovernedGroup
              label="Notification kinds"
              active={notificationsEnabledPref()}
            >
            <div class="den-settings-pref-row" {...settingAnchor("notify-finished")}>
              <div class="den-settings-pref-copy">
                <span class="den-settings-pref-label">{settingLabel("notify-finished")}</span>
                <p class="den-settings-hint">
                  Notify when a meaningful agent run completes.
                </p>
              </div>
              <DenCheckbox
                checked={notificationFinishedPref()}
                disabled={!initialized()}
                data-testid="notifications-finished"
                onChange={(e) => {
                  void saveNotificationFinished(e.currentTarget.checked);
                }}
              >
                <span class="sr-only">{settingLabel("notify-finished")}</span>
              </DenCheckbox>
            </div>
            <div class="den-settings-pref-row" {...settingAnchor("notify-needs-input")}>
              <div class="den-settings-pref-copy">
                <span class="den-settings-pref-label">{settingLabel("notify-needs-input")}</span>
                <p class="den-settings-hint">
                  Notify when the agent is waiting on your answer.
                </p>
              </div>
              <DenCheckbox
                checked={notificationNeedsYouPref()}
                disabled={!initialized()}
                data-testid="notifications-needs-you"
                onChange={(e) => {
                  void saveNotificationNeedsYou(e.currentTarget.checked);
                }}
              >
                <span class="sr-only">{settingLabel("notify-needs-input")}</span>
              </DenCheckbox>
            </div>
            <div class="den-settings-pref-row" {...settingAnchor("notify-needs-approval")}>
              <div class="den-settings-pref-copy">
                <span class="den-settings-pref-label">{settingLabel("notify-needs-approval")}</span>
                <p class="den-settings-hint">
                  Notify when a checkpoint is waiting for approval.
                </p>
              </div>
              <DenCheckbox
                checked={notificationNeedsApprovalPref()}
                disabled={!initialized()}
                data-testid="notifications-needs-approval"
                onChange={(e) => {
                  void saveNotificationNeedsApproval(e.currentTarget.checked);
                }}
              >
                <span class="sr-only">{settingLabel("notify-needs-approval")}</span>
              </DenCheckbox>
            </div>
            </SettingsGovernedGroup>
          </div>

          <DenButton
            variant="secondary"
            {...settingAnchor("test-notification")}
            data-testid="notifications-send-test"
            disabled={!initialized()}
            onClick={() => {
              void (async () => {
                const permission = await ensureNotificationPermission();
                recordNotificationPermission(permission);
                if (permission !== "granted") return;
                await sendNotification({
                  title: NOTIFICATION_PRODUCT_TITLE,
                  body: "Finished — test notification",
                });
              })();
            }}
          >
            {settingLabel("test-notification")}
          </DenButton>
          <Show when={notificationPermissionResult() === "unavailable"}>
            <p
              class="den-settings-hint"
              data-testid="notifications-shell-unavailable"
            >
              Notifications are unavailable right now.
            </p>
          </Show>
          <Show when={notificationPermissionResult() === "denied"}>
            <p
              class="den-settings-hint"
              data-testid="notifications-approval-denied"
            >
              macOS notifications are turned off for Painted Wolf Code — enable
              them in System Settings › Notifications.
            </p>
          </Show>
        </div>
      </Show>

      <Show when={displayedTab === "editor"}>
        <div
          id="general-panel-editor"
          role="tabpanel"
          aria-labelledby="general-tab-editor"
          data-testid="general-panel-editor"
        >
          <EditorSettingsPanel />
        </div>
      </Show>

      <Show when={displayedTab === "keyboard"}>
        <div
          id="general-panel-keyboard"
          role="tabpanel"
          aria-labelledby="general-tab-keyboard"
          data-testid="general-panel-keyboard"
        >
          <KeyboardSettingsPanel />
        </div>
      </Show>

      <Show when={displayedTab === "power"}>
        <div
          id="general-panel-power"
          role="tabpanel"
          aria-labelledby="general-tab-power"
          data-testid="general-panel-power"
        >
          <PowerSettingsPanel client={props.client} />
        </div>
      </Show>

      <Show when={displayedTab === "updates"}>
        <div
          id="general-panel-updates"
          role="tabpanel"
          aria-labelledby="general-tab-updates"
          data-testid="general-panel-updates"
        >
          <UpdatesSettingsPanel updateService={props.updateService} />
        </div>
      </Show>

      <Show when={displayedTab === "about"}>
        <div
          id="general-panel-about"
          role="tabpanel"
          aria-labelledby="general-tab-about"
          data-testid="general-panel-about"
        >
          <AboutSettingsPanel version={props.version} />
        </div>
      </Show>
        </>}
      </SurfaceDeck>
    </div>
  );
}
