import "../../test/document-outbox-fixture.ts";
import { DocumentFixture } from "../../test/document-fixture.ts";
import { connectFilesEditorDocuments } from "../../files/editor/files-editor-synchronization.ts";
import { editorReplica, resolveEditorDocument, resetEditorDocumentsForTests } from "../../files/documents/editor-document.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { createRoot, createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createChatBlueprintState } from "./chat-blueprint-state.ts";
import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError } from "../../api/http.ts";
import type {
  Blueprint,
  Project,
  WorkflowRun,
  WorkflowSummary,
} from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { overlayRel } from "../../platform/files/overlay-dir.ts";
import { emptyProjects } from "../../test/projects-fixture.ts";
import { applyFilesBufferDraft, applyFilesBufferLoad, applyFilesBufferEditorDocument, openFilesBuffer, resetProjectFilesForTests } from "../../files/documents/project-files-buffers.ts";
import { projectFilesState } from "../../files/documents/files-buffer-state.ts";
import {
  filesTreeCollapsed,
  resetFilesTreeWindowStateForTests,
} from "../../files/tree/files-tree-window-state.ts";
import { resetBlueprintWorkspaceOffersForTests } from "../../blueprint/blueprint-workspace-modal.ts";

const mocks = vi.hoisted(() => ({ client: null as LycaonClient | null }));

// Reset module-scoped offers between fixtures.
afterEach(() => {
  mocks.client = null;
  resetBlueprintWorkspaceOffersForTests();
  resetEditorDocumentsForTests();
});

vi.mock("../../platform/connection/app-connection.ts", () => ({
  getLycaonClient: () => mocks.client,
}));

function minimalStore(overrides: Partial<AppStore["state"]> = {}): AppStore {
  return {
    state: {
      messages: [],
      workflowRuns: [],
      ...overrides,
    },
    actions: { reportError: vi.fn() },
  } as unknown as AppStore;
}

const BLUEPRINT_PATH = overlayRel("blueprints", "plan-1.md");

/** The host's path filter answers the bound blueprint's summary. */
const listBlueprintAtPath = () =>
  vi.fn(async () => [
    {
      id: "bp-1",
      path: BLUEPRINT_PATH,
      title: "Plan",
      status: "draft" as const,
      version: 1,
      updated_at: "2025-01-01T00:00:00Z",
    },
  ]);

const planUi = {
  current_phase_label: "Waiting for your approval",
  human_approval_awaiting: true,
} satisfies NonNullable<WorkflowRun["ui"]>;

const planRun = {
  id: "run-1",
  workflow_id: "plan",
  workflow_version: "1.0.0",
  revision: 1,
  status: "running",
  blueprint_path: BLUEPRINT_PATH,
  ui: planUi,
} as WorkflowRun;

// Blueprint actions follow the catalog entry.
const planCatalog: readonly WorkflowSummary[] = [
  { id: "plan", version: "1.0.0", name: "Plan", supports_blueprints: true },
];

function createBlueprintState(overrides: {
  catalogRun?: () => WorkflowRun | null;
  catalog?: () => readonly WorkflowSummary[];
  onOpenFiles?: () => void;
  appStore?: AppStore;
  projects?: readonly Project[];
  clientOrThrow?: () => LycaonClient;
  withWorkflow?: (fn: () => Promise<void>) => void;
  exitWorkflow?: (reason?: string) => void;
} = {}) {
  return createRoot((dispose) => {
    const state = createChatBlueprintState({
      appStore: overrides.appStore ?? minimalStore(),
      sessionId: () => "sess-1",
      projectDir: () => "/repo",
      catalogRun: overrides.catalogRun ?? (() => planRun),
      catalog: overrides.catalog ?? (() => planCatalog),
      workflowsOpen: () => false,
      setWorkflowsOpen: () => {},
      withWorkflow: overrides.withWorkflow ?? ((fn) => void fn()),
      projects: overrides.projects ?? emptyProjects,
      clientOrThrow:
        overrides.clientOrThrow ??
        (() => {
          throw new Error("no client");
        }),
      onOpenFiles: overrides.onOpenFiles ?? (() => {}),
      exitWorkflow: overrides.exitWorkflow ?? (() => {}),
    });
    return { state, dispose };
  });
}

describe("createChatBlueprintState", () => {
  it.each(["approved", "canceled", "complete", "replaced"] as const)("clears revision warnings when review is %s", async (transition) => {
    const [run, setRun] = createSignal<WorkflowRun>({ ...planRun, ui: { ...planUi, plan_revision_at: "r1" } });
    const { state, dispose } = createBlueprintState({ catalogRun: run });
    try {
      await Promise.resolve();
      setRun({ ...run(), ui: { ...planUi, ...run().ui, plan_revision_at: "r2" } });
      await Promise.resolve();
      expect(state.blueprintChangedWarning()).toBe(true);
      expect(state.blueprintWarningFor(BLUEPRINT_PATH)).not.toBeNull();
      if (transition === "replaced") {
        setRun({ ...run(), id: "another-run", ui: { ...planUi, ...run().ui, plan_revision_at: "other-revision" } });
      } else {
        setRun({ ...run(), status: transition === "approved" ? "running" : transition, ui: { ...planUi, ...run().ui, human_approval_awaiting: false } });
      }
      await Promise.resolve();
      expect(state.blueprintChangedWarning()).toBe(false);
      expect(state.warning()).toBeNull();
      expect(state.blueprintWarningFor(BLUEPRINT_PATH)).toBeNull();
      setRun({ ...run(), ui: { ...planUi, ...run().ui, human_approval_awaiting: true } });
      await Promise.resolve();
      expect(state.warning()).toBeNull();
    } finally { dispose(); }
  });

    it("rejects by ending the run and closing the workspace", () => {
    const reasons: (string | undefined)[] = [];
    const { state, dispose } = createBlueprintState({
      exitWorkflow: (reason) => reasons.push(reason),
    });
    state.openReview();
    state.reject();
    expect(state.reviewOpen()).toBe(false);
    expect(reasons).toHaveLength(1);
    expect(reasons[0]).toContain("rejected");
    dispose();
  });

  it("derives awaitingApproval from host WorkflowRun.ui flag", () => {
    const { state, dispose } = createBlueprintState();
    expect(state.awaitingApproval()).toBe(true);
    dispose();
  });

  it("gates approval on host human_approval_awaiting only", () => {
    const { state, dispose } = createBlueprintState();
    expect(state.canApprove()).toBe(true);
    dispose();
  });

  it("cannot approve when host is not awaiting approval", () => {
    const { state, dispose } = createBlueprintState({
      catalogRun: () =>
        ({
          ...planRun,
          ui: { human_approval_awaiting: false },
        }) as WorkflowRun,
    });
    expect(state.canApprove()).toBe(false);
    dispose();
  });

  it("exposes composer placeholder during approval gate", () => {
    const { state, dispose } = createBlueprintState();
    expect(state.composerPlaceholder()).toBe(
      "Request changes here — approve on the blueprint card",
    );
    dispose();
  });

  it("passes full choice_transitions array from host ui", () => {
    const { state, dispose } = createBlueprintState({
      catalogRun: () =>
        ({
          ...planRun,
          ui: {
            human_approval_awaiting: true,
            choice_transitions: [
              { id: "deepen", label: "Deepen research", armed: true },
              { id: "critique", label: "Run critique", armed: false },
            ],
          },
        }) as WorkflowRun,
    });
    expect(state.choiceTransitions()).toEqual([
      { id: "deepen", label: "Deepen research", armed: true },
      { id: "critique", label: "Run critique", armed: false },
    ]);
    dispose();
  });

  it("keeps the blueprint text through run patches and refreshes it in place", async () => {
    const blueprint = (content: string): Blueprint => ({
      id: "bp-1",
      project_id: "project-1",
      path: BLUEPRINT_PATH,
      title: "Plan",
      content,
      status: "draft",
      version: 1,
      updated_at: "2025-01-01T00:00:00Z",
    });
    let resolveRevised: (plan: Blueprint) => void = () => {};
    const getBlueprint = vi
      .fn<() => Promise<Blueprint>>()
      .mockResolvedValueOnce(blueprint("# Plan\n"))
      .mockImplementationOnce(
        () => new Promise<Blueprint>((resolve) => { resolveRevised = resolve; }),
      );
    mocks.client = stubClient({ getBlueprint, listBlueprints: listBlueprintAtPath() });
    const [run, setRun] = createSignal<WorkflowRun | null>(
      { ...planRun, ui: { ...planUi, plan_revision_at: "r1" } } as WorkflowRun,
    );
    const { state, dispose } = createBlueprintState({ catalogRun: run });
    try {
      expect(state.blueprintLoadingFor(BLUEPRINT_PATH)).toBe(true);
      await vi.waitFor(() => expect(state.blueprintContentFor(BLUEPRINT_PATH)).toBe("# Plan\n"));
      expect(state.blueprintLoadingFor(BLUEPRINT_PATH)).toBe(false);

      // Run updates preserve the loaded blueprint text.
      setRun({ ...run()!, revision: 2 } as WorkflowRun);
      await Promise.resolve();
      expect(getBlueprint).toHaveBeenCalledTimes(1);
      expect(state.blueprintLoadingFor(BLUEPRINT_PATH)).toBe(false);
      expect(state.blueprintContentFor(BLUEPRINT_PATH)).toBe("# Plan\n");

      // A revision refetches; the current text stays up until the new one lands.
      setRun({ ...run()!, ui: { ...planUi, plan_revision_at: "r2" } } as WorkflowRun);
      await vi.waitFor(() => expect(getBlueprint).toHaveBeenCalledTimes(2));
      expect(state.blueprintLoadingFor(BLUEPRINT_PATH)).toBe(false);
      expect(state.blueprintContentFor(BLUEPRINT_PATH)).toBe("# Plan\n");
      resolveRevised(blueprint("# Plan v2\n"));
      await vi.waitFor(() => expect(state.blueprintContentFor(BLUEPRINT_PATH)).toBe("# Plan v2\n"));

      // The record outlives its run.
      setRun(null);
      await Promise.resolve();
      expect(state.blueprintContentFor(BLUEPRINT_PATH)).toBe("# Plan v2\n");
      expect(state.blueprintLoadingFor(BLUEPRINT_PATH)).toBe(false);
    } finally {
      dispose();
    }
  });

  it("follows the run blueprint_path when the host retargets", () => {
    const nextPath = overlayRel("blueprints", "ship-it.md");
    const [run, setRun] = createSignal<WorkflowRun | null>(planRun);
    const { state, dispose } = createBlueprintState({ catalogRun: run });
    expect(state.selectedBlueprintPath()).toBe(BLUEPRINT_PATH);
    setRun({ ...planRun, blueprint_path: nextPath } as WorkflowRun);
    expect(state.selectedBlueprintPath()).toBe(nextPath);
    dispose();
  });

  it("opens the workspace when the run reaches the ask", () => {
    const [run, setRun] = createSignal<WorkflowRun | null>({
      ...planRun,
      ui: { human_approval_awaiting: false },
    } as WorkflowRun);
    const { state, dispose } = createBlueprintState({ catalogRun: run });
    expect(state.reviewOpen()).toBe(false);
    setRun({ ...planRun, ui: { human_approval_awaiting: true } } as WorkflowRun);
    expect(state.reviewOpen()).toBe(true);
    expect(state.reviewView()).toBe("preview");
    dispose();
  });

  // An existing ask opens its blueprint after remount.
  it("opens the workspace for an ask that was already waiting", () => {
    const { state, dispose } = createBlueprintState();
    expect(state.reviewOpen()).toBe(true);
    dispose();
  });

  it("does not put the same ask up twice after it is closed", () => {
    const first = createBlueprintState();
    expect(first.state.reviewOpen()).toBe(true);
    first.state.closeReview();
    first.dispose();

    // A remount — a stage crossfade, or a trip to Files and back.
    const second = createBlueprintState();
    expect(second.state.reviewOpen()).toBe(false);
    second.dispose();
  });

  it("puts a revised blueprint up again — it is a new ask", () => {
    const first = createBlueprintState({
      catalogRun: () =>
        ({ ...planRun, ui: { ...planUi, plan_revision_at: "r1" } }) as WorkflowRun,
    });
    expect(first.state.reviewOpen()).toBe(true);
    first.state.closeReview();
    first.dispose();

    const second = createBlueprintState({
      catalogRun: () =>
        ({ ...planRun, ui: { ...planRun.ui, plan_revision_at: "r2" } }) as WorkflowRun,
    });
    expect(second.state.reviewOpen()).toBe(true);
    second.dispose();
  });

  it("openReview carries the requested initial Files markdown view", () => {
    const { state, dispose } = createBlueprintState();
    state.openReview({ view: "code" });
    expect(state.reviewOpen()).toBe(true);
    expect(state.reviewView()).toBe("code");
    state.closeReview();
    expect(state.reviewOpen()).toBe(false);
    dispose();
  });

  it("transfers the open blueprint to Files and closes the review host", () => {
    resetFilesTreeWindowStateForTests();
    const onOpenFiles = vi.fn();
    const { state, dispose } = createBlueprintState({ onOpenFiles });
    state.openReview({ view: "code" });
    expect(state.selectedBlueprintPath()).toBe(BLUEPRINT_PATH);
    state.openInFiles();
    expect(state.reviewOpen()).toBe(false);
    expect(filesTreeCollapsed()).toBe(true);
    expect(onOpenFiles).toHaveBeenCalledOnce();
    resetFilesTreeWindowStateForTests();
    dispose();
  });

  it("falls back to the dirty Files buffer when the modal save cannot finish", async () => {
    resetProjectFilesForTests();
    const key = openFilesBuffer("project-1", {
      rootId: "root-1",
      rootLabel: "repo",
      path: BLUEPRINT_PATH,
      intent: "permanent",
    });
    applyFilesBufferLoad("project-1", key, {
      file_id: "",
      version_id: "",
      workspace_id: "workspace-1",
      workspace_kind: "project",
      root_id: "root-1",
      path: BLUEPRINT_PATH,
      content: "# Original\n",
      sha256: "sha-before",
      encoding: "utf-8",
      writable: true,
      over_limit: false,
      binary: false,
      size_bytes: 11,
    });
    applyFilesBufferEditorDocument("project-1", key, {
      encoding: "utf-8",
      fileId: "file-1",
      documentId: "document-1",
      revision: 1,
      diverged: false,
      absent: false,
      heldAgentVersionId: null,
      localGeneration: 1, dirty: false,
      baseSha256: "sha-before",
      eol: "lf",
      baseEol: "lf",
      mixedEol: false,
      baseMixedEol: false,
      sizeBytes: 11,
    });
    applyFilesBufferDraft("project-1", key, "# Edited\n");
    const order: string[] = [];
    const host = new DocumentFixture("# Original\n", { path: BLUEPRINT_PATH, base_sha256: "sha-before" });
    const client = stubClient({
      openEditorDocument: async () => host.snapshot(),
      syncEditorDocument: host.sync,
      submitEditorDocumentUpdate: host.submit,
      leaveEditorDocument: async () => undefined,
      createEditorDocumentSnapshot: async () => host.snapshot(),
      saveEditorDocument: vi.fn(async () => {
        order.push("save");
        host.wire.base_content = host.text.toString();
        host.wire.base_sha256 = "sha-after";
        host.wire.dirty = false;
        host.wire.revision++;
        return host.snapshot();
      }),
      listBlueprints: listBlueprintAtPath(),
      getBlueprint: vi.fn(async () => ({
        id: "bp-1",
        path: BLUEPRINT_PATH,
        title: "Plan",
        content: "# Edited\n",
        status: "draft",
        updated_at: "2025-01-01T00:00:00Z",
      })),
      approveBlueprint: vi.fn(async () => {
        order.push("approve");
        return {};
      }),
    });
    const appStore = minimalStore({
      currentSession: {
        id: "sess-1",
        project_id: "project-1",
      } as AppStore["state"]["currentSession"],
    });
    const projects = [
      {
        id: "project-1",
        roots: [
          {
            id: "root-1",
            path: "/repo",
            is_primary: true,
          },
        ],
      },
    ] as Project[];
    const { state, dispose } = createBlueprintState({
      appStore,
      projects,
      clientOrThrow: () => client,
      withWorkflow: (fn) => void fn().catch(() => undefined),
    });

    connectFilesEditorDocuments("project-1", () => client, () => "sess-1");
    await resolveEditorDocument("project-1", projectFilesState("project-1").byKey[key]!);
    editorReplica("document-1")!.replaceLocal("# Edited\n");
    applyFilesBufferDraft("project-1", key, "# Edited\n");
    const modalSave = vi.fn(async () => false);
    state.setDocumentSession({ save: modalSave });
    state.approve();
    await vi.waitFor(() => expect(client.approveBlueprint).toHaveBeenCalled());
    expect(modalSave).toHaveBeenCalledOnce();
    expect(order.slice(0, 2)).toEqual(["save", "approve"]);
    dispose();
  });

  it("cannot approve while the run revision is not in hand", () => {
    const { state, dispose } = createBlueprintState({
      catalogRun: () => ({ ...planRun, revision: 0 }) as WorkflowRun,
    });
    expect(state.awaitingApproval()).toBe(true);
    expect(state.canApprove()).toBe(false);
    dispose();
  });

  it("maps an approve 409 to friendly copy instead of the raw host string", async () => {
    const errors: string[] = [];
    const client = stubClient({
      listBlueprints: listBlueprintAtPath(),
      getBlueprint: vi.fn(async () => ({
        id: "bp-1",
        path: BLUEPRINT_PATH,
        title: "Plan",
        content: "# Plan\n",
        status: "draft",
        updated_at: "2025-01-01T00:00:00Z",
      })),
      approveBlueprint: vi.fn(async () => {
        throw new LycaonApiError("workflow run revision mismatch", 409, "workflow_revision_conflict");
      }),
    });
    const { state, dispose } = createBlueprintState({
      appStore: minimalStore({
        currentSession: {
          id: "sess-1",
          project_id: "project-1",
        } as AppStore["state"]["currentSession"],
      }),
      clientOrThrow: () => client,
      withWorkflow: (fn) =>
        void fn().catch((err) => errors.push(err instanceof Error ? err.message : String(err))),
    });

    state.approve();
    await vi.waitFor(() => expect(errors).toHaveLength(1));
    expect(errors[0]).toBe("The blueprint changed. Review it and approve again.");
    dispose();
  });

  it("maps a choice-transition 409 to friendly copy", async () => {
    const errors: string[] = [];
    const client = stubClient({
      fireWorkflowTransition: vi.fn(async () => {
        throw new LycaonApiError("workflow run revision mismatch", 409, "workflow_revision_conflict");
      }),
    });
    const { state, dispose } = createBlueprintState({
      catalogRun: () =>
        ({
          ...planRun,
          ui: {
            human_approval_awaiting: true,
            choice_transitions: [{ id: "critique", label: "Run critique", armed: true }],
          },
        }) as WorkflowRun,
      clientOrThrow: () => client,
      withWorkflow: (fn) =>
        void fn().catch((err) => errors.push(err instanceof Error ? err.message : String(err))),
    });

    state.fireChoiceTransition("critique");
    await vi.waitFor(() => expect(errors).toHaveLength(1));
    expect(errors[0]).toBe("The workflow changed. Review it and try again.");
    dispose();
  });

  it("hides choice arms when not awaiting approval", () => {
    const { state, dispose } = createBlueprintState({
      catalogRun: () =>
        ({
          ...planRun,
          ui: {
            human_approval_awaiting: false,
            choice_transitions: [{ id: "critique", label: "Run critique", armed: true }],
          },
        }) as WorkflowRun,
    });
    expect(state.choiceTransitions()).toEqual([]);
    dispose();
  });
});
