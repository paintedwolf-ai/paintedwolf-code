import { Show, createMemo, type Accessor } from "solid-js";
import type { AppStore } from "../../store/app-state-model.ts";
import type { BlueprintSummary } from "../../api/types.ts";
import { sessionWorkers } from "../../chat/actions/chat-actions.ts";
import { gitChangeBadgeCount } from "../../chat/actions/board-git-coalesce.ts";
import { chatTabRailBindings } from "../../chat/composer/chat-tab-rail-bindings.ts";
import { liveProgressItems, isProgressArchivedInTranscript } from "../../chat/progress/progress-model.ts";
import { pendingWorkerApprovals } from "../../chat/worker/worker-approval-model.ts";
import {
  liveWorkerRows,
  sessionWorkerRows,
  type OpenWorkerOptions,
} from "../../chat/worker/workers-model.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { verboseModePref } from "../../settings/system/debug-prefs.ts";
import { isResolved, valueOf } from "../../store/load-state.ts";
import { catalogWorkflowRun } from "../../workflow/workflow-run-stack.ts";
import { tabBadge } from "../chatview/tabs.ts";
import { GitStrip } from "../GitStrip.tsx";
import { ProgressStrip } from "../ProgressStrip.tsx";
import { WorkersTab } from "../worker/WorkersTab.tsx";
import { WorkflowsTab } from "../workflow/WorkflowsTab.tsx";
import type { TabSpec } from "./TabBar.tsx";
import { IconProgress, IconWorkflows, IconGit, IconWorkers } from "./TabIcons.tsx";

export type SessionTabsOptions = {
  appStore: AppStore;
  sessionId: Accessor<string>;
  selectedWorkerId: Accessor<string | null>;
  onOpenWorker: (workerId?: string, opts?: OpenWorkerOptions) => void;
  /** Runs before an action whose result appears in the conversation. */
  showConversation?: () => void;
};

function blueprintsById(blueprints: readonly BlueprintSummary[]): Record<string, BlueprintSummary> {
  return Object.fromEntries(blueprints.map((p) => [p.path, p]));
}

export type SessionTabs = {
  tabs: Accessor<TabSpec[]>;
  /** The Git badge reflects a settled read. */
  gitResolved: Accessor<boolean>;
};

/** The session's Progress, Workflows, Git and Workers tabs, for whichever rail presents them. */
export function createSessionTabs(options: SessionTabsOptions): SessionTabs {
  const { appStore } = options;
  const inConversation = <Args extends unknown[], R>(action: (...args: Args) => R) =>
    (...args: Args): R => {
      options.showConversation?.();
      return action(...args);
    };

  // Memoize roster and transcript scans across stream envelopes.
  const workers = createMemo(() => sessionWorkers(appStore, options.sessionId()));
  const workerApprovals = createMemo(() =>
    pendingWorkerApprovals(workers(), appStore.state.pendingCheckpoints),
  );
  const liveWorkers = createMemo(() => liveWorkerRows(workers()));
  const rosterWorkers = createMemo(() =>
    sessionWorkerRows(workers(), { verboseMode: verboseModePref() }),
  );
  const progressSteps = createMemo(() => appStore.state.progress?.steps ?? []);
  const progressLiveItems = createMemo(() => liveProgressItems(progressSteps()));
  const progressArchivedInTranscript = createMemo(() =>
    isProgressArchivedInTranscript(progressSteps(), appStore.state.messages),
  );
  const sessionHydrating = () => appStore.state.chatHydrationLock != null;
  const catalogRun = createMemo(() =>
    catalogWorkflowRun(appStore.state.activeWorkflowRun, appStore.state.workflowRuns),
  );
  const gitChanges = createMemo(() =>
    gitChangeBadgeCount(valueOf(appStore.state.gitStatus), appStore.state.gitRepos),
  );
  // The repo set answers the badge on its own; otherwise a settled status read does.
  const gitResolved = () =>
    appStore.state.gitRepos !== undefined || isResolved(appStore.state.gitStatus);

  const bindings = () => chatTabRailBindings();

  const progressPanel = () => (
    <ProgressStrip
      embedded
      archivedInTranscript={progressArchivedInTranscript()}
      batchPhase={appStore.state.coordinatorRunContext?.batch_phase}
      steps={progressSteps()}
      activeMs={appStore.state.turnClock?.active_ms}
      running={appStore.state.turnClock?.running}
      runningSince={appStore.state.turnClock?.running_at}
      contextPrompt={appStore.state.contextUsage?.prompt}
      contextWindow={appStore.state.contextUsage?.window}
      compactionThreshold={appStore.state.contextUsage?.compactionThreshold}
      onOpenWorklog={inConversation(() => bindings()?.onOpenWorklog())}
      worklogOpen={bindings()?.worklogOpen() === true}
    />
  );

  const workflowsPanel = () => (
    <Show when={bindings()?.workflows} keyed>
      {(api) => (
        <WorkflowsTab
          activeRun={catalogRun()}
          catalog={appStore.state.workflowCatalog}
          blueprintsById={blueprintsById(appStore.state.blueprints)}
          pickerOpen={api.workflowPickerOpen()}
          error={api.workflowError()}
          busy={api.workflowBusy()}
          onExit={(run) => api.onExit(run)}
          onPause={() => api.onPause()}
          onResume={() => api.onResume()}
          onAdvance={() => api.onAdvance()}
          onReviewInChat={inConversation(() => api.onReviewInChat())}
          onOpenPicker={() => api.onOpenPicker()}
          onClosePicker={() => api.onClosePicker()}
          onArmWorkflow={(workflow) => api.onArmWorkflow(workflow)}
          onJumpToRun={inConversation((run) => api.onJumpToRun(run))}
          downloadReport={async (runId) => {
            const client = getLycaonClient();
            if (!client) throw new Error("offline");
            return client.getWorkflowRunReport(runId);
          }}
        />
      )}
    </Show>
  );

  const gitPanel = () => (
    <Show when={bindings()?.git} keyed>
      {(api) => (
        <GitStrip
          status={appStore.state.gitStatus}
          repos={appStore.state.gitRepos}
          activeRepoId={appStore.state.gitActiveRepoId}
          scopePin={appStore.state.gitScopePin}
          projectId={api.projectId}
          sessionId={options.sessionId()}
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
          onGroupCommit={inConversation(() => api.onGroupCommit())}
          onRefreshWorktree={() => api.onRefreshWorktree()}
          onBindWorktree={(repoId, branch) => api.onBindWorktree(repoId, branch)}
          onLandWorktree={() => api.onLandWorktree()}
          onUnbindWorktree={() => api.onUnbindWorktree()}
        />
      )}
    </Show>
  );

  const workersPanel = () => (
    <WorkersTab
      workers={workers()}
      pendingCheckpoints={appStore.state.pendingCheckpoints}
      selectedId={options.selectedWorkerId()}
      onSelect={inConversation((id) => options.onOpenWorker(id))}
      onCancel={(id) => bindings()?.onCancelWorker(id)}
      busyWorkerId={() => bindings()?.cancellingWorkerId() ?? null}
      verboseMode={verboseModePref()}
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
  return { tabs, gitResolved };
}
