import { createEffect, createSignal, type Accessor } from "solid-js";
import type {
  Blueprint,
  Project,
  WorkflowRun,
  WorkflowSummary,
} from "../../api/types.ts";
import { isApiErrorCode } from "../../api/http.ts";
import { refreshWorkflowState } from "../../chat/workflow/workflow-actions.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { boundBlueprintRelPath } from "../../blueprint/blueprint-path.ts";
import type { BlueprintChoiceTransition } from "../../blueprint/blueprint-inline-card-model.ts";
import {
  markBlueprintWorkspaceOffered,
  blueprintWorkspaceOffered,
  blueprintWorkspaceOfferKey,
  type BlueprintReviewView,
} from "../../blueprint/blueprint-workspace-modal.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { isBlueprintWorkflowRun } from "../../workflow/workflows-drawer-model.ts";
import type { ProjectFileDocumentSession } from "../../files/components/ProjectFilesView.tsx";
import { saveProjectFileDocument } from "../../files/documents/project-file-document-save.ts";
import { projectFilesState } from "../../files/documents/files-buffer-state.ts";
import { fileBufferKey } from "../../files/components/project-files-model.ts";
import { setFilesTreeCollapsed } from "../../files/tree/files-tree-window-state.ts";
import { transcriptViewportForSession } from "../../chat/stream/transcript-viewport.tsx";

/** Refusals that mean the reviewed run, blueprint, or choice moved under the reviewer. */
const STALE_REVIEW_CODES = [
  "workflow_revision_conflict",
  "workflow_run_not_active",
  "blueprint_content_conflict",
  "human_approval_not_ready",
  "invalid_workflow_transition",
  "choice_transition_not_armed",
  "choice_transition_pending_input",
] as const;

type LycaonClient = NonNullable<ReturnType<typeof getLycaonClient>>;

/** The chat binds blueprints by path; the host addresses them by id. */
async function blueprintAtPath(
  client: LycaonClient,
  projectId: string,
  path: string,
): Promise<Blueprint> {
  const [summary] = await client.listBlueprints(projectId, { path });
  if (!summary) throw new Error("This blueprint is no longer in the project.");
  return client.getBlueprint(projectId, summary.id);
}

export type ChatBlueprintStateDeps = {
  appStore: AppStore;
  projects: readonly Project[];
  sessionId: Accessor<string>;
  projectDir: Accessor<string>;
  catalogRun: Accessor<WorkflowRun | null | undefined>;
  /** Workflow catalog — decides which runs carry blueprint affordances. */
  catalog: Accessor<readonly WorkflowSummary[]>;
  workflowsOpen: Accessor<boolean>;
  setWorkflowsOpen: (open: boolean) => void;
  withWorkflow: (fn: () => Promise<void>) => void;
  clientOrThrow: () => NonNullable<ReturnType<typeof getLycaonClient>>;
  onOpenFiles: () => void;
  /** Ends the active run — the host action behind Reject. */
  exitWorkflow: (reason?: string) => void;
};

export type ChatBlueprintState = {
  name: Accessor<string | null>;
  reviewOpen: Accessor<boolean>;
  reviewView: Accessor<BlueprintReviewView>;
  error: Accessor<string | null>;
  warning: Accessor<string | null>;
  activeBlueprintPath: Accessor<string | null>;
  selectedBlueprintPath: Accessor<string | null>;
  awaitingApproval: Accessor<boolean>;
  canApprove: Accessor<boolean>;
  choiceTransitions: Accessor<readonly BlueprintChoiceTransition[]>;
  blueprintChangedWarning: Accessor<boolean>;
  reject: () => void;
  composerPlaceholder: Accessor<string | undefined>;
  openReview: (opts?: { blueprintPath?: string; view?: BlueprintReviewView }) => void;
  closeReview: () => void;
  openInFiles: () => void;
  setDocumentSession: (session: ProjectFileDocumentSession | null) => void;
  savePendingEdits: () => Promise<boolean>;
  approve: (blueprintPath?: string) => void;
  fireChoiceTransition: (transitionId: string) => void;
  scrollToBlueprintCard: () => void;
  bindBlueprintPath: (blueprintPath: string) => void;
  blueprintWarningFor: (blueprintPath: string) => string | null;
  blueprintLoadingFor: (blueprintPath: string) => boolean;
  blueprintContentFor: (blueprintPath: string) => string | null;
};

async function sha256Hex(value: string): Promise<string> {
  const digest = await globalThis.crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

export function createChatBlueprintState(deps: ChatBlueprintStateDeps): ChatBlueprintState {
  // Loaded blueprints outlive their run; a run patch never swaps text for the placeholder.
  const [blueprints, setBlueprints] = createSignal<ReadonlyMap<string, Blueprint>>(new Map());
  const [pendingKeys, setPendingKeys] = createSignal<ReadonlySet<string>>(new Set());
  const [reviewOpen, setReviewOpen] = createSignal(false);
  const [reviewView, setReviewView] = createSignal<BlueprintReviewView>("preview");
  const [error, setError] = createSignal<string | null>(null);
  const [revisionChangedAtReview, setRevisionChangedAtReview] = createSignal(false);
  const [returnToDrawerOnEditEnd, setReturnToDrawerOnEditEnd] = createSignal(false);
  const [boundBlueprintPath, setBoundBlueprintPath] = createSignal<string | null>(null);

  let lastBlueprintRevision = "";
  let lastRevisionAwaiting = false;
  let lastBlueprintScope = "";
  let documentSession: ProjectFileDocumentSession | null = null;

  /** The catalog run when its recipe declares a blueprint, else null. */
  const blueprintRun = (): WorkflowRun | null => {
    const run = deps.catalogRun();
    return isBlueprintWorkflowRun(run, deps.catalog()) ? (run ?? null) : null;
  };

  // The bound run path wins so a retargeted file stays the open document.
  const activeBlueprintPath = () => {
    const runPath = blueprintRun()?.blueprint_path?.trim();
    if (runPath) return runPath;
    return boundBlueprintPath()?.trim() || null;
  };

  const projectId = () => {
    const fromSession = deps.appStore.state.currentSession?.project_id?.trim();
    if (fromSession) return fromSession;
    const dir = deps.projectDir()?.trim();
    if (!dir) return "";
    const hit = deps.projects.find((p) =>
      (p.roots ?? []).some((r) => r.path === dir || dir.startsWith(r.path + "/")),
    );
    return hit?.id?.trim() ?? "";
  };

  const selectedBlueprintPath = () => boundBlueprintRelPath(activeBlueprintPath()) || null;

  const awaitingApproval = () => {
    if (!blueprintRun()) return false;
    return deps.catalogRun()?.ui?.human_approval_awaiting === true;
  };

  // Approving needs the run's revision for stale-command rejection, so the
  // button only arms once that revision is actually in hand.
  const canApprove = () => {
    if (!awaitingApproval()) return false;
    const run = deps.catalogRun();
    return Boolean(run?.id?.trim() && run.revision);
  };

  /** Refreshes stale run state and rethrows a stale review as friendly copy. */
  const rethrowRevisionConflict = async (
    err: unknown,
    copy: string,
  ): Promise<never> => {
    if (isApiErrorCode(err, STALE_REVIEW_CODES)) {
      await refreshWorkflowState(
        deps.appStore,
        deps.clientOrThrow(),
        deps.sessionId(),
        deps.projectDir(),
        deps.projects,
      ).catch(() => undefined);
      throw new Error(copy);
    }
    throw err;
  };

  const choiceTransitions = () => {
    if (!awaitingApproval()) return [];
    const raw = deps.catalogRun()?.ui?.choice_transitions;
    if (!raw?.length) return [];
    return raw.map((arm) => ({
      id: arm.id,
      label: arm.label,
      armed: arm.armed,
    }));
  };

  const blueprintChangedWarning = () => awaitingApproval() && revisionChangedAtReview();
  const warning = () => blueprintChangedWarning() ? "Blueprint changed — approve again to implement." : null;

  const composerPlaceholder = () => {
    if (!awaitingApproval()) return undefined;
    return "Request changes here — approve on the blueprint card";
  };

  // The same relative path in two projects names different blueprints.
  const blueprintKey = (blueprintPath: string) => `${projectId()}\0${blueprintPath.trim()}`;
  const blueprintFor = (blueprintPath: string | null) =>
    blueprintPath ? blueprints().get(blueprintKey(blueprintPath)) ?? null : null;
  const rememberBlueprint = (blueprintPath: string, blueprint: Blueprint) => {
    const key = blueprintKey(blueprintPath);
    setBlueprints((prior) => new Map(prior).set(key, blueprint));
  };
  const name = () => blueprintFor(activeBlueprintPath())?.title ?? null;

  const blueprintContentFor = (blueprintPath: string) => blueprintFor(blueprintPath)?.content ?? null;

  /** The placeholder stands in only while there is nothing to show. */
  const blueprintLoadingFor = (blueprintPath: string) => {
    const key = blueprintKey(blueprintPath);
    return pendingKeys().has(key) && !blueprints().has(key);
  };

  const fetchBlueprint = (blueprintPath: string) => {
    const client = getLycaonClient();
    if (!client) return;
    const key = blueprintKey(blueprintPath);
    setPendingKeys((prior) => new Set(prior).add(key));
    void blueprintAtPath(client, projectId(), blueprintPath)
      .then((blueprint) => rememberBlueprint(blueprintPath, blueprint))
      .catch(() => undefined)
      .finally(() => {
        setPendingKeys((prior) => {
          const next = new Set(prior);
          next.delete(key);
          return next;
        });
      });
  };

  createEffect(() => {
    const scope = JSON.stringify([deps.sessionId(), blueprintRun()?.id, activeBlueprintPath()]);
    if (scope !== lastBlueprintScope) {
      lastBlueprintScope = scope;
      lastBlueprintRevision = "";
      lastRevisionAwaiting = false;
      setRevisionChangedAtReview(false);
    }
    if (!awaitingApproval()) setRevisionChangedAtReview(false);
    const rev = deps.catalogRun()?.ui?.plan_revision_at ?? "";
    if (!rev) return;
    const blueprintPath = activeBlueprintPath();
    const revisionChanged = Boolean(lastBlueprintRevision && rev !== lastBlueprintRevision);
    // Changes during approval require a fresh decision.
    if (revisionChanged && awaitingApproval() && lastRevisionAwaiting) {
      setRevisionChangedAtReview(true);
    }
    // A revised blueprint replaces its text in place.
    if (revisionChanged && blueprintPath) fetchBlueprint(blueprintPath);
    lastBlueprintRevision = rev;
    lastRevisionAwaiting = awaitingApproval();
  });

  // Show each approval revision once its path is ready.
  createEffect(() => {
    const run = blueprintRun();
    const runId = run?.id?.trim();
    if (!runId || !awaitingApproval() || !selectedBlueprintPath()) return;
    const key = blueprintWorkspaceOfferKey(runId, run?.ui?.plan_revision_at);
    if (blueprintWorkspaceOffered(key)) return;
    markBlueprintWorkspaceOffered(key);
    if (!reviewOpen()) openReview({ view: "preview" });
  });

  // Run events replace the run object; only a scope change refetches.
  let fetchedScope = "";
  createEffect(() => {
    const blueprintPath = activeBlueprintPath();
    const scope = blueprintPath && getLycaonClient() && blueprintRun() ? blueprintKey(blueprintPath) : "";
    if (scope === fetchedScope) return;
    fetchedScope = scope;
    if (blueprintPath && scope) fetchBlueprint(blueprintPath);
  });

  const bindBlueprintPath = (blueprintPath: string) => {
    const id = blueprintPath.trim();
    if (id) setBoundBlueprintPath(id);
  };

  const blueprintWarningFor = (blueprintPath: string) =>
    activeBlueprintPath() === blueprintPath.trim() ? warning() : null;

  const finishReview = (reopenDrawer: boolean) => {
    setReviewOpen(false);
    setReviewView("preview");
    setReturnToDrawerOnEditEnd(false);
    if (reopenDrawer) {
      deps.setWorkflowsOpen(true);
    }
  };

  const openReview = (opts?: { blueprintPath?: string; view?: BlueprintReviewView }) => {
    if (opts?.blueprintPath?.trim()) setBoundBlueprintPath(opts.blueprintPath.trim());
    const returnToDrawer = deps.workflowsOpen() && !awaitingApproval();
    setError(null);
    setReturnToDrawerOnEditEnd(returnToDrawer);
    if (returnToDrawer) {
      deps.setWorkflowsOpen(false);
    }
    setReviewView(opts?.view ?? "preview");
    setReviewOpen(true);
  };

  const closeReview = () => finishReview(returnToDrawerOnEditEnd());

  const openInFiles = () => {
    if (!selectedBlueprintPath()) return;
    finishReview(false);
    setFilesTreeCollapsed(true);
    deps.onOpenFiles();
  };

  const setDocumentSession = (session: ProjectFileDocumentSession | null) => {
    documentSession = session;
  };

  const documentBufferKey = () => {
    const path = selectedBlueprintPath();
    const project = deps.projects.find((entry) => entry.id === projectId());
    const root =
      project?.roots.find((entry) => entry.is_primary) ?? project?.roots[0];
    return path && root ? fileBufferKey(root.id, path) : null;
  };

  // Host actions save the current Files draft first.
  const flushPendingEdits = async (): Promise<boolean> => {
    setError(null);
    if (documentSession && (await documentSession.save())) return true;
    const key = documentBufferKey();
    if (!key || !projectFilesState(projectId()).byKey[key]?.dirty) return true;
    const result = await saveProjectFileDocument({
      client: deps.clientOrThrow(),
      projectId: projectId(),
      key,
      sessionId: deps.sessionId(),
    });
    if (result.status === "saved" || result.status === "clean") return true;
    setError(result.message);
    return false;
  };

  const approve = (blueprintPath?: string) =>
    deps.withWorkflow(async () => {
      if (blueprintPath?.trim()) setBoundBlueprintPath(blueprintPath.trim());
      const sessionId = deps.sessionId();
      const id = activeBlueprintPath();
      if (!sessionId || !id || !canApprove()) return;
      setError(null);
      if (!(await flushPendingEdits())) return;
      const client = deps.clientOrThrow();
      const reviewed = await blueprintAtPath(client, projectId(), id);
      rememberBlueprint(id, reviewed);
      const run = deps.catalogRun();
      if (!run?.id || !run.revision) {
        // canApprove() gated this, so only a run swap mid-flight lands here.
        throw new Error("The workflow changed. Review it and try again.");
      }
      try {
        await client.approveBlueprint(projectId(), reviewed.id, {
          workflow_run_id: run.id,
          expected_revision: run.revision,
          content_digest: await sha256Hex(reviewed.content),
        });
      } catch (err) {
        await rethrowRevisionConflict(
          err,
          "The blueprint changed. Review it and approve again.",
        );
      }
      finishReview(false);
      await refreshWorkflowState(
        deps.appStore,
        deps.clientOrThrow(),
        sessionId,
        deps.projectDir(),
        deps.projects,
      );
    });

  // Ending the run leaves the blueprint file available for another run.
  const reject = () => {
    setError(null);
    finishReview(false);
    deps.exitWorkflow("Blueprint rejected at review");
  };

  const fireChoiceTransition = (transitionId: string) =>
    deps.withWorkflow(async () => {
      const sessionId = deps.sessionId();
      const run = deps.catalogRun();
      const runId = run?.id?.trim();
      const id = transitionId.trim();
      if (!sessionId || !run || !runId || !id || !awaitingApproval()) return;
      setError(null);
      if (!(await flushPendingEdits())) return;
      try {
        await deps.clientOrThrow().fireWorkflowTransition(runId, id, {
          expected_revision: run.revision,
        });
      } catch (err) {
        await rethrowRevisionConflict(
          err,
          "The workflow changed. Review it and try again.",
        );
      }
      finishReview(false);
      await refreshWorkflowState(
        deps.appStore,
        deps.clientOrThrow(),
        sessionId,
        deps.projectDir(),
        deps.projects,
      );
    });

  const scrollToBlueprintCard = () => {
    const card = document.querySelector<HTMLElement>('[data-testid="blueprint-card"]');
    const viewport = transcriptViewportForSession(deps.sessionId());
    if (card) viewport?.ensureVisible(card, { align: "center" });
    const body = card?.querySelector('[data-testid="blueprint-card-body"]');
    if (!body) {
      card
        ?.querySelector<HTMLButtonElement>('[data-testid="blueprint-card-toggle"]')
        ?.click();
    }
  };

  return {
    name,
    reviewOpen,
    reviewView,
    error,
    warning,
    activeBlueprintPath,
    selectedBlueprintPath,
    awaitingApproval,
    canApprove,
    choiceTransitions,
    blueprintChangedWarning,
    reject,
    composerPlaceholder,
    openReview,
    closeReview,
    openInFiles,
    setDocumentSession,
    savePendingEdits: flushPendingEdits,
    approve,
    fireChoiceTransition,
    scrollToBlueprintCard,
    bindBlueprintPath,
    blueprintWarningFor,
    blueprintLoadingFor,
    blueprintContentFor,
  };
}
