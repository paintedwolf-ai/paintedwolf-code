import { Show, createMemo, type JSX } from "solid-js";
import type { AppStore } from "../../store/app-state-model.ts";
import { gitStatusRefreshPending } from "../../chat/actions/git-status-reads.ts";
import { useChatTabChrome } from "../../chat/composer/chat-tab-chrome.tsx";
import type { OpenWorkerOptions } from "../../chat/worker/workers-model.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { openRunningStage } from "../../platform/navigation/open-running.ts";
import { projectWatchesFor } from "../../watches/watches-store.ts";
import { sessionExportOverflowItems, messagesHaveExportableTranscript } from "../../session/session-export-menu.ts";
import { exportSessionTranscript } from "../../settings/storage/data-backup-actions.ts";
import { usePresentationParticipant } from "../../ui/presentation-context.tsx";
import { BrowseOverflowMenu } from "../browse/BrowseOverflowMenu.tsx";
import { ContextPaneToggle } from "./ContextPaneToggle.tsx";
import { ConversationCollapseButton } from "./ConversationToggle.tsx";
import {
  NavSidebarAutoRestoreButton,
  NavSidebarExpandButton,
} from "./NavSidebarToggle.tsx";
import { createSessionTabs } from "./session-tabs.tsx";
import { SplitFitRestoreButton } from "./SplitFitRestore.tsx";
import { TabBar } from "./TabBar.tsx";

export type ChatTabRailProps = {
  appStore: AppStore;
  sessionId: string;
  selectedWorkerId?: string | null;
  onOpenWorker: (workerId?: string, opts?: OpenWorkerOptions) => void;
  worklogOpen: boolean;
  onOpenWorklog: () => void;
  navCollapsed?: boolean;
  navAutomaticallyCollapsed?: boolean;
  /** Rendered in split chat, but CSS reveals it only when the stage yields. */
  navSplitFallback?: boolean;
  navSide?: "left" | "right";
  onNavExpand?: () => void;
  /** Seam controls of a live split whose conversation is showing. */
  conversationSeam?: {
    /** Window edge the conversation occupies; its seam is the opposite end. */
    side: "left" | "right";
    onHide: () => void;
    contextHidden: boolean;
    onRestoreContext: () => void;
    /** Stage the seam faces, named in the widen control. */
    stageLabel: string;
    onWidenForStage: () => void;
    /** Refusals of that width claim, marked on the control that made it. */
    widenRefusals: () => number;
  };
  /** Top chrome dock; the rail reserves space under the chips. */
  dock?: JSX.Element;
  /** Drawn window controls when the conversation holds a window edge. */
  windowControlsStart?: JSX.Element;
  windowControlsEnd?: JSX.Element;
};

export function ChatTabRail(props: ChatTabRailProps) {
  const chrome = useChatTabChrome();
  const { tabs, gitResolved } = createSessionTabs({
    appStore: props.appStore,
    sessionId: () => props.sessionId,
    selectedWorkerId: () => props.selectedWorkerId ?? null,
    onOpenWorker: (workerId, opts) => props.onOpenWorker(workerId, opts),
    worklogOpen: () => props.worklogOpen,
    onOpenWorklog: () => props.onOpenWorklog(),
  });
  // Holds only across a read in flight; an unstarted read cannot hold the surface.
  usePresentationParticipant(
    "git-status",
    () => gitResolved() || !gitStatusRefreshPending(props.appStore),
  );

  const canExportConversation = createMemo(() =>
    messagesHaveExportableTranscript(props.appStore.state.messages),
  );

  const onExportConversation = (format: "md" | "json") => {
    const client = getLycaonClient();
    if (!client) return;
    void exportSessionTranscript(client, props.sessionId, format);
  };

  const pairedRestore = () => props.navCollapsed && props.conversationSeam != null &&
    props.navSide !== props.conversationSeam.side;
  const navExpand = () => {
    const control = props.navAutomaticallyCollapsed ? (
      <NavSidebarAutoRestoreButton
        labelled={pairedRestore()}
        side={props.navSide}
        onClick={() => props.onNavExpand?.()}
      />
    ) : (
      <NavSidebarExpandButton
        labelled={pairedRestore()}
        side={props.navSide}
        onClick={() => props.onNavExpand?.()}
      />
    );
    return props.navSplitFallback ? (
      <span class="den-nav-split-fallback">{control}</span>
    ) : (
      control
    );
  };
  const navOnRight = () => props.navSide === "right";
  const navLeads = () => props.navCollapsed === true && !navOnRight();
  // The seam is the end facing the stage.
  const seamAt = (slot: "leading" | "trailing") => {
    const side = props.conversationSeam?.side;
    return slot === "leading" ? side === "right" : side === "left";
  };
  // Narrow bands replace a hide with a width claim; explicit hides use a restore.
  const seamControls = () => (
    <Show when={props.conversationSeam} keyed>
      {(seam) => (
        <Show when={seam.contextHidden} fallback={
          <>
            <ConversationCollapseButton side={seam.side} onClick={seam.onHide} />
            <SplitFitRestoreButton
              pane="stage"
              labelled={pairedRestore()}
              label={seam.stageLabel}
              side={seam.side === "right" ? "left" : "right"}
              refusals={seam.widenRefusals}
              onClick={seam.onWidenForStage}
            />
          </>
        }>
          <ContextPaneToggle
            hidden
            labelled={pairedRestore()}
            side={seam.side === "right" ? "left" : "right"}
            onClick={seam.onRestoreContext}
          />
        </Show>
      )}
    </Show>
  );

  const exportMenu = () => (
    <BrowseOverflowMenu
      testId="session-export-menu"
      label="Export conversation"
      variant="icon"
      disabled={!canExportConversation()}
      items={sessionExportOverflowItems({ onExport: onExportConversation })}
    />
  );

  const standingWatchOrigin = createMemo(() => {
    const sess = props.appStore.state.currentSession;
    if (!sess || sess.id !== props.sessionId) return null;
    const origin = sess.ui?.standing_watch;
    if (!origin) return null;
    const projectId = sess.project_id ?? "";
    const view = projectId ? projectWatchesFor(projectId) : null;
    const deleted =
      origin.deleted === true ||
      (view?.hydrated === true && !view.standingById.has(origin.watch_id));
    return { ...origin, projectId, deleted };
  });

  const standingWatchBadge = () => (
    <Show when={standingWatchOrigin()} keyed>
      {(origin) => (
        <Show
          when={!origin.deleted}
          fallback={
            <span
              class="px-den-2 text-den-caption text-den-text-muted truncate max-w-52"
              data-testid="chat-header-standing-watch-deleted"
            >
              Deleted standing watch · {origin.watch_name}
            </span>
          }
        >
          <button
            type="button"
            class="den-chat-content-link px-den-2 text-den-caption truncate max-w-52"
            data-testid="chat-header-standing-watch-link"
            onClick={() =>
              openRunningStage({
                projectId: origin.projectId || undefined,
                watchId: origin.watch_id,
              })
            }
          >
            {origin.watch_name}
          </button>
        </Show>
      )}
    </Show>
  );

  return (
    <TabBar
      open={chrome.openTab()}
      onOpenChange={chrome.selectTab}
      retracted={chrome.panelRetracted()}
      onPanelHeightChange={chrome.syncPanelHeight}
      dock={props.dock}
      windowControlsStart={props.windowControlsStart}
      windowControlsEnd={props.windowControlsEnd}
      leading={
        navLeads() || props.conversationSeam ? (
          <>
            <Show when={navLeads()}>{navExpand()}</Show>
            <Show when={seamAt("trailing")}>{exportMenu()}</Show>
            <Show when={seamAt("leading")}>{seamControls()}</Show>
          </>
        ) : undefined
      }
      trailing={
        <>
          {standingWatchBadge()}
          <Show when={!seamAt("trailing")}>{exportMenu()}</Show>
          <Show when={seamAt("trailing")}>{seamControls()}</Show>
          <Show when={props.navCollapsed && navOnRight()}>{navExpand()}</Show>
        </>
      }
      tabs={tabs()}
    />
  );
}
