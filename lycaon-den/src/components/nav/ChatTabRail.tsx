import { Show, createMemo, type JSX } from "solid-js";
import type { AppStore } from "../../store/app-state-model.ts";
import { sessionWorkers } from "../../chat/actions/chat-actions.ts";
import { liveProgressItems, isProgressArchivedInTranscript } from "../../chat/progress/progress-model.ts";
import { catalogWorkflowRun } from "../../workflow/workflow-run-stack.ts";
import { tabBadge } from "../chatview/tabs.ts";
import {
  liveWorkerRows,
  sessionWorkerRows,
  type OpenWorkerOptions,
} from "../../chat/worker/workers-model.ts";
import { pendingWorkerApprovals } from "../../chat/worker/worker-approval-model.ts";
import { useChatTabChrome } from "../../chat/composer/chat-tab-chrome.tsx";
import { chatTabRailBindings } from "../../chat/composer/chat-tab-rail-bindings.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { verboseModePref } from "../../settings/system/debug-prefs.ts";
import { exportSessionTranscript } from "../../settings/storage/data-backup-actions.ts";
import { sessionExportOverflowItems, messagesHaveExportableTranscript } from "../../session/session-export-menu.ts";
import { TabBar, type TabSpec } from "./TabBar.tsx";
import { IconProgress, IconWorkflows, IconGit, IconWorkers } from "./TabIcons.tsx";
import {
  NavSidebarAutoRestoreButton,
  NavSidebarExpandButton,
} from "./NavSidebarToggle.tsx";
import { ContextPaneToggle } from "./ContextPaneToggle.tsx";
import { ConversationCollapseButton } from "./ConversationToggle.tsx";
import { SplitFitRestoreButton } from "./SplitFitRestore.tsx";
import { tabBarTrailingControls } from "../shell/WindowControls.tsx";
import { ProgressStrip } from "../ProgressStrip.tsx";
import { WorkflowsTab } from "../workflow/WorkflowsTab.tsx";
import { GitStrip } from "../GitStrip.tsx";
import { WorkersTab } from "../worker/WorkersTab.tsx";
import type { BlueprintSummary } from "../../api/types.ts";
import { gitChangeBadgeCount } from "../../chat/actions/board-git-coalesce.ts";
import { gitStatusRefreshPending } from "../../chat/actions/git-status-reads.ts";
import { isResolved, valueOf } from "../../store/load-state.ts";
import { usePresentationParticipant } from "../../ui/presentation-context.tsx";
import { BrowseOverflowMenu } from "../browse/BrowseOverflowMenu.tsx";

export type ChatTabRailProps = {
  appStore: AppStore;
  sessionId: string;
  selectedWorkerId?: string | null;
  onOpenWorker: (workerId?: string, opts?: OpenWorkerOptions) => void;
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
};

function blueprintsById(blueprints: readonly BlueprintSummary[]): Record<string, BlueprintSummary> {
  return Object.fromEntries(blueprints.map((p) => [p.path, p]));
}

export function ChatTabRail(props: ChatTabRailProps) {
  const chrome = useChatTabChrome();

  // Memoize roster and transcript scans across stream envelopes.
  const workers = createMemo(() =>
    sessionWorkers(props.appStore, props.sessionId),
  );
  const workerApprovals = createMemo(() =>
    pendingWorkerApprovals(workers(), props.appStore.state.pendingCheckpoints),
  );
  const liveWorkers = createMemo(() => liveWorkerRows(workers()));
  const rosterWorkers = createMemo(() =>
    sessionWorkerRows(workers(), { verboseMode: verboseModePref() }),
  );
  const progressSteps = createMemo(
    () => props.appStore.state.progress?.steps ?? [],
  );
  const progressLiveItems = createMemo(() => liveProgressItems(progressSteps()));
  const progressArchivedInTranscript = createMemo(() =>
    isProgressArchivedInTranscript(
      progressSteps(),
      props.appStore.state.messages,
    ),
  );
  const sessionHydrating = () => props.appStore.state.chatHydrationLock != null;
  const batchPhase = () => props.appStore.state.coordinatorRunContext?.batch_phase;
  const catalogRun = createMemo(() =>
    catalogWorkflowRun(
      props.appStore.state.activeWorkflowRun,
      props.appStore.state.workflowRuns,
    ),
  );
  const gitChanges = createMemo(() =>
    gitChangeBadgeCount(
      valueOf(props.appStore.state.gitStatus),
      props.appStore.state.gitRepos,
    ),
  );
  // The repo set answers the badge on its own; otherwise a settled status read does.
  const gitResolved = () =>
    props.appStore.state.gitRepos !== undefined ||
    isResolved(props.appStore.state.gitStatus);
  // Holds only across a read in flight; an unstarted read cannot hold the surface.
  usePresentationParticipant(
    "git-status",
    () => gitResolved() || !gitStatusRefreshPending(props.appStore),
  );

  const bindings = () => chatTabRailBindings();

  const canExportConversation = createMemo(() =>
    messagesHaveExportableTranscript(props.appStore.state.messages),
  );

  const onExportConversation = (format: "md" | "json") => {
    const client = getLycaonClient();
    if (!client) return;
    void exportSessionTranscript(client, props.sessionId, format);
  };

  const windowTrailing = () => tabBarTrailingControls();

  const progressPanel = () => (
    <ProgressStrip
      embedded
      archivedInTranscript={progressArchivedInTranscript()}
      batchPhase={batchPhase()}
      steps={progressSteps()}
      activeMs={props.appStore.state.turnClock?.active_ms}
      running={props.appStore.state.turnClock?.running}
      runningSince={props.appStore.state.turnClock?.running_at}
      contextPrompt={props.appStore.state.contextUsage?.prompt}
      contextWindow={props.appStore.state.contextUsage?.window}
      compactionThreshold={
        props.appStore.state.contextUsage?.compactionThreshold
      }
      onOpenWorklog={() => bindings()?.onOpenWorklog()}
      worklogOpen={bindings()?.worklogOpen() === true}
    />
  );

  const workflowsPanel = () => {
    const wf = bindings()?.workflows;
    return (
      <Show when={wf} keyed>
        {(api) => (
          <WorkflowsTab
            activeRun={catalogRun()}
            catalog={props.appStore.state.workflowCatalog}
            blueprintsById={blueprintsById(props.appStore.state.blueprints)}
            pickerOpen={api.workflowPickerOpen()}
            error={api.workflowError()}
            busy={api.workflowBusy()}
            onExit={(run) => api.onExit(run)}
            onPause={() => api.onPause()}
            onResume={() => api.onResume()}
            onAdvance={() => api.onAdvance()}
            onReviewInChat={() => api.onReviewInChat()}
            onOpenPicker={() => api.onOpenPicker()}
            onClosePicker={() => api.onClosePicker()}
            onArmWorkflow={(workflow) => api.onArmWorkflow(workflow)}
            onJumpToRun={(run) => api.onJumpToRun(run)}
            downloadReport={async (runId) => {
              const client = getLycaonClient();
              if (!client) throw new Error("offline");
              return client.getWorkflowRunReport(runId);
            }}
          />
        )}
      </Show>
    );
  };

  const gitPanel = () => {
    const g = bindings()?.git;
    return (
      <Show when={g} keyed>
        {(api) => (
          <GitStrip
            status={props.appStore.state.gitStatus}
            repos={props.appStore.state.gitRepos}
            activeRepoId={props.appStore.state.gitActiveRepoId}
            scopePin={props.appStore.state.gitScopePin}
            projectId={api.projectId}
            sessionId={props.sessionId}
            rootRefs={api.rootRefs}
            busy={api.busy}
            onRefreshRepos={() => api.onRefreshRepos()}
            onScopePin={(pin) => api.onScopePin(pin)}
            onSelectRepo={(repoId) => api.onSelectRepo(repoId)}
            onInit={(rootId) => api.onInit(rootId)}
            onCommit={(message) => api.onCommit(message)}
            onStash={() => api.onStash()}
            onDraftMessage={() => api.onDraftMessage()}
            onListBranches={() => api.onListBranches()}
            onCheckout={(branch, create) => api.onCheckout(branch, create)}
            onDiscard={() => api.onDiscard()}
            onPush={() => api.onPush()}
            onPull={() => api.onPull()}
            onGroupCommit={() => api.onGroupCommit()}
            onRefreshWorktree={() => api.onRefreshWorktree()}
            onBindWorktree={(repoId, branch) =>
              api.onBindWorktree(repoId, branch)
            }
            onLandWorktree={() => api.onLandWorktree()}
            onUnbindWorktree={() => api.onUnbindWorktree()}
          />
        )}
      </Show>
    );
  };

  const workersPanel = () => (
    <WorkersTab
      workers={workers()}
      pendingCheckpoints={props.appStore.state.pendingCheckpoints}
      selectedId={props.selectedWorkerId ?? null}
      onSelect={(id) => props.onOpenWorker(id)}
      onCancel={(id) => bindings()?.onCancelWorker(id)}
      busyWorkerId={() => bindings()?.cancellingWorkerId() ?? null}
      verboseMode={verboseModePref()}
    />
  );

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

  // Icons are created once; a badge change must not re-create them.
  const icons = {
    progress: <IconProgress />,
    workflows: <IconWorkflows />,
    git: <IconGit />,
    workers: <IconWorkers />,
  };
  const tabs = createMemo<TabSpec[]>(() => [
    {
      id: "progress",
      label: "Progress",
      icon: icons.progress,
      badge: tabBadge(progressLiveItems().length),
      panel: progressPanel,
    },
    {
      id: "workflows",
      label: "Workflows",
      firstTimeTip: "workflows",
      icon: icons.workflows,
      active: catalogRun() != null,
      panel: workflowsPanel,
    },
    {
      id: "git",
      label: "Git",
      icon: icons.git,
      badge: tabBadge(gitChanges()),
      // An unresolved status is not "no changes".
      empty: !sessionHydrating() && gitResolved() && gitChanges() === 0,
      panel: gitPanel,
    },
    {
      id: "workers",
      label: "Workers",
      icon: icons.workers,
      badge: tabBadge(workerApprovals().length || liveWorkers().length),
      active: workerApprovals().length > 0,
      empty: !sessionHydrating() && rosterWorkers().length === 0,
      panel: workersPanel,
    },
  ]);

  return (
    <TabBar
      open={chrome.openTab()}
      onOpenChange={chrome.selectTab}
      retracted={chrome.panelRetracted()}
      onPanelHeightChange={chrome.syncPanelHeight}
      dock={props.dock}
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
          <Show when={!seamAt("trailing")}>{exportMenu()}</Show>
          <Show when={seamAt("trailing")}>{seamControls()}</Show>
          <Show when={props.navCollapsed && navOnRight()}>{navExpand()}</Show>
          {windowTrailing()}
        </>
      }
      tabs={tabs()}
    />
  );
}
