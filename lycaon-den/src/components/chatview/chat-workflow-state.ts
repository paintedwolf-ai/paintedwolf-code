import { createEffect, createSignal, type Accessor } from "solid-js";
import type { PendingWorkflowStart, WorkflowRun, WorkflowSummary } from "../../api/types.ts";
import {
  advanceActiveWorkflow,
  exitActiveWorkflow,
  pauseActiveWorkflow,
  resumeActiveWorkflow,
  startWorkflowForSession,
} from "../../chat/workflow/workflow-actions.ts";
import {
  armedWorkflowFromPickerRow,
  type ArmedWorkflow,
} from "../../chat/workflow/workflow-arm.ts";
import { clearPendingArmWorkflow } from "../../chat/workflow/pending-arm.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import {
  catalogEntryForWorkflow,
  type CatalogPickerRow,
} from "../../workflow/workflows-drawer-model.ts";
import type { Project } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";

export type ChatWorkflowStateDeps = {
  appStore: AppStore;
  projects: readonly Project[];
  sessionId: Accessor<string>;
  projectDir: Accessor<string>;
  catalogRun: Accessor<WorkflowRun | null | undefined>;
  workflowsOpen: Accessor<boolean>;
  setWorkflowsOpen: (open: boolean) => void;
  scrollToRun: (run: WorkflowRun) => void;
  clientOrThrow: () => NonNullable<ReturnType<typeof getLycaonClient>>;
  /** Bump composer focus after arming from a tile / drawer / bare slash. */
  onArmed?: () => void;
};

export type ChatWorkflowState = {
  pickerOpen: Accessor<boolean>;
  busy: Accessor<boolean>;
  error: Accessor<string | null>;
  armed: Accessor<ArmedWorkflow | null>;
  startProposal: Accessor<PendingWorkflowStart | null | undefined>;
  withWorkflow: (fn: () => Promise<void>) => void;
  openPicker: () => void;
  closePicker: () => void;
  dismissProposal: () => void;
  openDrawerWithPicker: () => void;
  /** Open or close the Workflows tab (session-launcher More tile). */
  toggleDrawerWithPicker: () => void;
  /** Arm a catalog workflow — start on the next composer send. */
  armWorkflow: (workflow: WorkflowSummary | CatalogPickerRow) => void;
  setArmed: (armed: ArmedWorkflow | null) => void;
  clearArmed: () => void;
  /** Coordinator proposal Start — immediate (thread already has context). */
  startProposalWorkflow: (proposal: PendingWorkflowStart) => void;
  runControl: (
    action: (
      appStore: AppStore,
      client: ReturnType<ChatWorkflowStateDeps["clientOrThrow"]>,
      sessionId: string,
      projectDir: string,
      projects: readonly Project[],
    ) => Promise<unknown>,
  ) => void;
  /** Optional reason is the host's ledger note for why the run ended. */
  bindExit: (reason?: string, target?: WorkflowRun) => void;
  bindPause: () => void;
  bindResume: () => void;
  bindAdvance: () => void;
  bindJumpToRun: (run: WorkflowRun) => void;
};

export function createChatWorkflowState(
  deps: ChatWorkflowStateDeps,
): ChatWorkflowState {
  const [pickerOpen, setPickerOpen] = createSignal(false);
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal<string | null>(null);
  const [armed, setArmed] = createSignal<ArmedWorkflow | null>(null);
  const [proposalDismissed, setProposalDismissed] = createSignal(false);

  const startProposal = () => {
    if (proposalDismissed() || deps.catalogRun()) return null;
    return deps.appStore.state.currentSession?.ui?.pending_workflow_start ?? null;
  };

  createEffect(() => {
    deps.sessionId();
    setProposalDismissed(false);
    setArmed(null);
  });

  // Clears only for this session's catalog run; a run still held from the previous
  // session leaves a pending Blueprints arm in place.
  createEffect(() => {
    const run = deps.catalogRun();
    if (!run) return;
    if (run.session_id !== deps.sessionId()) return;
    setProposalDismissed(false);
    setArmed(null);
    clearPendingArmWorkflow();
  });

  const withWorkflow = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const runControl = (
    action: (
      appStore: AppStore,
      client: ReturnType<ChatWorkflowStateDeps["clientOrThrow"]>,
      sessionId: string,
      projectDir: string,
      projects: readonly Project[],
    ) => Promise<unknown>,
  ) =>
    void withWorkflow(() => {
      const sessionId = deps.sessionId();
      if (!sessionId) return Promise.resolve();
      return action(
        deps.appStore,
        deps.clientOrThrow(),
        sessionId,
        deps.projectDir(),
        deps.projects,
      ).then(() => undefined);
    });

  const armWorkflow = (wf: CatalogPickerRow) => {
    const next = armedWorkflowFromPickerRow(wf);
    if (!next) {
      setError(`Workflow ${wf.id} has no slash trigger to arm.`);
      return;
    }
    const current = armed();
    if (
      current &&
      current.workflow_id === next.workflow_id &&
      (current.preset_id ?? "") === (next.preset_id ?? "")
    ) {
      setArmed(null);
      return;
    }
    setError(null);
    setArmed(next);
    setPickerOpen(false);
    deps.onArmed?.();
  };

  const startProposalWorkflow = (proposal: PendingWorkflowStart) =>
    void withWorkflow(async () => {
      const entry = catalogEntryForWorkflow(
        deps.appStore.state.workflowCatalog,
        proposal.workflow_id,
        proposal.workflow_version,
        proposal.preset_id,
      );
      if (!entry) {
        setError(`Workflow ${proposal.workflow_id} is not in the catalog.`);
        return;
      }
      setArmed(null);
      await startWorkflowForSession(
        deps.appStore,
        deps.clientOrThrow(),
        deps.sessionId(),
        deps.projectDir(),
        deps.projects,
        {
          workflow_id: entry.id,
          workflow_version: entry.version ?? proposal.workflow_version,
          preset_id: entry.start_preset_id ?? proposal.preset_id,
        },
      );
      setProposalDismissed(true);
    });

  return {
    pickerOpen,
    busy,
    error,
    armed,
    startProposal,
    withWorkflow: (fn) => void withWorkflow(fn),
    openPicker: () => setPickerOpen(true),
    closePicker: () => setPickerOpen(false),
    dismissProposal: () => setProposalDismissed(true),
    openDrawerWithPicker: () => {
      deps.setWorkflowsOpen(true);
      setPickerOpen(true);
    },
    toggleDrawerWithPicker: () => {
      if (deps.workflowsOpen()) {
        deps.setWorkflowsOpen(false);
        setPickerOpen(false);
        return;
      }
      deps.setWorkflowsOpen(true);
      setPickerOpen(true);
    },
    armWorkflow,
    setArmed: (next) => {
      setArmed(next);
      if (next) deps.onArmed?.();
    },
    clearArmed: () => {
      setArmed(null);
      clearPendingArmWorkflow();
    },
    startProposalWorkflow,
    runControl,
	bindExit: (reason, target) =>
      runControl((appStore, client, sessionId, projectDir, projects) =>
		exitActiveWorkflow(appStore, client, sessionId, projectDir, projects, reason, target),
      ),
    bindPause: () => runControl(pauseActiveWorkflow),
    bindResume: () => runControl(resumeActiveWorkflow),
    bindAdvance: () => runControl(advanceActiveWorkflow),
    bindJumpToRun: (run) => deps.scrollToRun(run),
  };
}
