import type { Accessor } from "solid-js";
import type { WorkflowRun, WorkflowSummary } from "../../api/types.ts";
import type { GitScope } from "../../components/git-repo-scope.ts";
import { createClaimable } from "../../platform/interaction/claimable.ts";

export type ChatTabWorkflowsBinding = {
  workflowPickerOpen: Accessor<boolean>;
  workflowError: Accessor<string | null>;
  workflowBusy: Accessor<boolean>;
  onExit: (run: WorkflowRun) => void;
  onPause: () => void;
  onResume: () => void;
  onAdvance: () => void;
  onReviewInChat: () => void;
  onOpenPicker: () => void;
  onClosePicker: () => void;
  /** Arm a catalog workflow — start on the next composer send. */
  onArmWorkflow: (workflow: WorkflowSummary) => void;
  onJumpToRun: (run: import("../../api/types.ts").WorkflowRun) => void;
};

export type ChatTabGitBinding = {
  projectId: string;
  rootRefs: readonly import("../../api/project-path.ts").ResolveProjectRoot[];
  /** True while a Git-tab mutation is in flight. */
  busy: Accessor<boolean>;
  onRefreshRepos: () => void;
  onScopePin: (pin: GitScope | null) => void;
  onSelectRepo: (repoId: string) => void;
  onInit: (rootId: string) => void;
  onCommit: (message: string) => Promise<boolean>;
  onStash: () => void;
  onDraftMessage: () => Promise<string>;
  onListBranches: () => Promise<import("../../api/types.ts").GitBranchEntry[]>;
  onCheckout: (branch: string, create: boolean) => Promise<void>;
  onDiscard: () => void;
  onPush: () => void;
  onPull: () => void;
  /** Ask the coordinator to commit all uncommitted work in logical groups. */
  onGroupCommit: () => void;
  onRefreshWorktree: () => Promise<
    import("../../api/types.ts").GitWorktreeView | undefined
  >;
  onBindWorktree: (
    repoId: string,
    branch?: string,
  ) => Promise<import("../../api/types.ts").GitWorktreeView>;
  onLandWorktree: () => Promise<
    import("../../chat/actions/git-actions.ts").WorktreeLandOutcome
  >;
  onUnbindWorktree: () => Promise<void>;
};

export type ChatTabRailBindings = {
  workflows: ChatTabWorkflowsBinding;
  git: ChatTabGitBinding;
  onOpenWorklog: () => void;
  /** Called by the shell's Escape handler. */
  onCloseWorklog: () => void;
  worklogOpen: Accessor<boolean>;
  onCancelWorker: (workerId: string) => void;
  /** Worker awaiting cancellation acknowledgement. */
  cancellingWorkerId: Accessor<string | null>;
};

const bindings = createClaimable<ChatTabRailBindings>();

/** Only the owning resident chat can release its bindings. */
export function setChatTabRailBindings(
  next: ChatTabRailBindings | null,
  claimToken: object,
): void {
  if (next !== null) bindings.claim(next, claimToken);
  else bindings.release(claimToken);
}

export function chatTabRailBindings(): ChatTabRailBindings | null {
  return bindings.get();
}
