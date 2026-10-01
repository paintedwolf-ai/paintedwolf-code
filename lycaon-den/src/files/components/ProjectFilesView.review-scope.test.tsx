import "../../test/document-outbox-fixture.ts";
import { required } from "../../test/at.ts";
import { filesBufferText } from "../documents/project-files-buffers.ts";
import { filesBufferBase } from "../documents/project-files-buffers.ts";
import { deletedSourceFixture } from "../../test/source-reader-fixture.ts";
import { sourceEffectFixture } from "../source/source-effect-fixture.ts";
import { stubFilesClient } from "../../test/source-client-fixture.ts";
import type { ClientStubs } from "../../test/client-fixture.ts";
import {
  PROJECT,
  ROOTS,
  resetProjectFilesViewTest,
  sourceWorkspace,
} from "./project-files-view-test-harness.ts";

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { EditorView } from "@codemirror/view";
import { ProjectFilesView } from "./ProjectFilesView.tsx";
import { createAppStore } from "../../store/app-state.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { LycaonApiError } from "../../api/http.ts";
import type { ProjectSourceReadResponse } from "../../api/types.ts";
import { applyFilesBufferDraft, applyFilesBufferLoad, openFilesBuffer } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { openSourceInFilesStage } from "./project-files-open.ts";
import { fileBufferKey } from "./project-files-model.ts";
import { findFileRow } from "../../test/files-tree-queries.ts";
import { previewDiffBufferJobId } from "../review/review-model.ts";
import { fileVersionFromSnapshot } from "../history/file-version.ts";
import {
  setFilesStagePaneMode,
  setSidebarScope,
} from "../review/review-pane.ts";
import { isDeletedInScope } from "../tree/scope-resolution.ts";
import { gitReview } from "../review/git-review-fixtures.ts";
import {
  enterWalk,
  isWalking,
  leaveWalk,
  setWalkAt,
  walkState,
} from "../walk/walk-store.ts";

describe("Review files use the current editor", () => {
  const changeFor = (path: string, id: string) => ({
    file_id: `file-${path}`,
    root_id: "r1",
    path,
    changed_since_presented: true,
    unpresented_agent_effects: 1,
    tip: { state: "content", sha256: `${id}-sha` },
    effects: [
      sourceEffectFixture({
        id,
        project_id: PROJECT,
        operation_id: `operation-${id}`,
        file_id: `file-${path}`,
        after_version_id: `version-${id}`,
        workspace_kind: "project" as const,
        root_id: "r1",
        path,
        op: "write",
        entry_kind: "file",
        origin: "agent",
        turn: 2,
        ordinal: 1,
        cause: "tool",
        capture_quality: "exact",
        observed_at: "2026-08-06T10:00:00Z",
      }),
    ],
  });

  const renderView = (
    client: ClientStubs,
    opts: { sessionId?: string } = {},
  ) => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    if (opts.sessionId) {
      appStore.actions.setCurrentSession({
        id: opts.sessionId,
        project_id: PROJECT,
        current_turn: 2,
      } as never);
    }
    const viewClient = stubFilesClient({
      ...getLycaonClient()!,
      getSourceWorkspace: vi.fn(async () => sourceWorkspace()),
      browseProjectSource: vi.fn(async (_projectId, input) => ({
        workspace_id: "workspace-1", root_id: input.rootId, dir: input.dir,
        watch_complete: true, entries: [],
      })),
      listProjectSourcePins: vi.fn(async () => ({ pins: [] })),
      listProjectSourceVersions: vi.fn(async () => ({
        file_id: "file-main",
        current: { state: "content" as const, sha256: "base-sha" },
        versions: [],
      })),
      getProjectSourceStorage: vi.fn(async () => ({
        inventory: {
          status: "ready",
          complete: true,
          roots_generation: 1,
          indexed_files: 0,
        },
        storage: {
          lanes: [{ lane: "source_blobs" as const, scope: "device" as const, used_bytes: 0 }],
        },
      })),
      listProjectSourceSymbols: vi.fn(async () => ({ symbols: [] })),
      ...client,
    });
    return Object.assign(render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={viewClient}
      />
    )), { appStore });
  };

  beforeEach(resetProjectFilesViewTest);

  it.each([false, true])("presents a commit-only walk when Files mounts after entry: %s", async (enterBeforeMount) => {
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(400);
    const client = stubFilesClient({
      listProjectSourceWalk: vi.fn(async () => ({
        baseline: "session:s1", files: [], commands: [], commit_available: true,
        git_changes: [{ id: "commit", root_id: "r1", kind: "commit", ordinal: 10,
          ts: "2026-09-18T04:20:00Z", session_id: "s1", turn: 1,
          tool_call_id: "commit-call", tool_name: "git_commit" }],
        turns: [{ session_id: "s1", turn: 1, message_id: "user-1",
          prompt: "Check in logical groups", ts: "2026-09-18T04:15:00Z" }],
      })),
      getProjectSourceGitReview: vi.fn(async () => gitReview([])),
    });
    const enter = () => enterWalk(PROJECT, client, "s1", undefined,
      { transition: "replace", startMessageId: "user-1" });
    if (enterBeforeMount) {
      openFilesBuffer(PROJECT, { rootId: "r1", rootLabel: "repo", path: "src/main.ts", intent: "permanent" });
      await enter();
    }
    const { unmount } = renderView(client, { sessionId: "s1" });
    if (!enterBeforeMount) await enter();
    await waitFor(() => expect(projectFilesState(PROJECT).activeKey).toBe("walk:git:commit"));
    await waitFor(() => expect(screen.getByTestId("step-bar").dataset.boot).toBe("ready"));
    await waitFor(() => expect(screen.getByTestId("walk-page").closest("[data-resident]")?.getAttribute("data-resident")).toBe("active"));
    expect(screen.getByTestId("walk-page").closest("[inert]")).toBeNull();
    await enter();
    await waitFor(() => expect(screen.getByTestId("step-bar").dataset.boot).toBe("ready"));
    expect(screen.getByTestId("walk-page").closest("[inert]")).toBeNull();
    unmount();

    const laterFile = openFilesBuffer(PROJECT, {
      rootId: "r1", rootLabel: "repo", path: "src/later.ts", intent: "permanent",
    });
    const remounted = renderView(client, { sessionId: "s1" });
    expect(projectFilesState(PROJECT).pendingKey ?? projectFilesState(PROJECT).activeKey).toBe(laterFile);
    remounted.unmount();
  });

  it("reads the chat's current turn again when a new turn starts, from one request shape", async () => {
    setSidebarScope(PROJECT, { kind: "turn" });
    const listProjectSourceWalk = vi.fn(async (_p: string, opts: { baseline: string }) => ({
      baseline: opts.baseline === "turn:s1" ? "turn:s1,2" : opts.baseline,
      files: [], commit_available: false, git_changes: [], commands: [], turns: [],
    }));
    const { appStore, unmount } = renderView({ listProjectSourceWalk }, { sessionId: "s1" });
    const turnReads = () => listProjectSourceWalk.mock.calls.filter(([, opts]) => opts.baseline === "turn:s1");
    await waitFor(() => expect(turnReads().length).toBeGreaterThan(0));
    const before = turnReads().length;

    appStore.actions.setCurrentSession({ id: "s1", project_id: PROJECT, current_turn: 3 } as never);

    await waitFor(() => expect(turnReads().length).toBeGreaterThan(before));
    for (const [, opts] of listProjectSourceWalk.mock.calls) {
      expect(opts).toMatchObject({ sessionId: "s1" });
    }
    unmount();
  });

  it("does not re-ask a checkout comparison when another chat sharing the checkout is selected", async () => {
    setSidebarScope(PROJECT, { kind: "commit" });
    const listProjectSourceWalk = vi.fn(async (_p: string, opts: { baseline: string }) => ({
      baseline: opts.baseline,
      files: [], commit_available: false, git_changes: [], commands: [], turns: [],
    }));
    const getSourceWorkspace = vi.fn(async () => sourceWorkspace());
    const { appStore, unmount } = renderView({ listProjectSourceWalk, getSourceWorkspace }, { sessionId: "s1" });
    // Once the host says the project checkout answers, no chat is part of the question.
    await waitFor(() => expect(listProjectSourceWalk.mock.calls.at(-1)?.[1]).not.toHaveProperty("sessionId", "s1"));
    const asked = listProjectSourceWalk.mock.calls.length;
    const resolved = getSourceWorkspace.mock.calls.length;

    appStore.actions.setCurrentSession({ id: "s2", project_id: PROJECT, current_turn: 5 } as never);
    await waitFor(() => expect(getSourceWorkspace.mock.calls.length).toBeGreaterThan(resolved));
    await Promise.resolve();

    expect(listProjectSourceWalk).toHaveBeenCalledTimes(asked);
    unmount();
  });

  const walkClient = (stepPath: string) => {
    const anchored = (path: string, id: string) => ({
      ...changeFor(path, id),
      effects: [{
        ...changeFor(path, id).effects[0]!,
        tool_call_id: "t1",
        tool_name: "write",
      }],
    });
    return stubFilesClient({
      listProjectSourceWalk: vi.fn(
        async (_p: string, opts: { baseline: string }) => ({
          files: opts.baseline.startsWith("session:")
            ? [anchored(stepPath, "walk-change")]
            : [changeFor("src/main.ts", "seen-change")],
          commit_available: false,
          git_changes: [], commands: [], turns: [],
        }),
      ),
      readComparison: vi.fn(async () => ({
        in_range: true,
        before: { state: "content", size_bytes: 4, availability: "available", content: "old\n" },
        after: { state: "content", size_bytes: 4, availability: "available", content: "new\n", sha256: "walk-change-sha" },
        location_changed: false,
      })),
    });
  };

  it("opens the Walk step in its ordinary file buffer without replacing an open tab", async () => {
    const fileKey = openFilesBuffer(PROJECT, {
      rootId: "r1",
      rootLabel: "repo",
      fileId: "file-src/main.ts",
      path: "src/main.ts",
      intent: "permanent",
    });
    const client = walkClient("src/main.ts");
    const { unmount } = renderView(client, { sessionId: "s1" });

    await enterWalk(PROJECT, client, "s1");
    await waitFor(() => {
      expect(projectFilesState(PROJECT).activeKey).toBe(fileKey);
    });
    expect(projectFilesState(PROJECT).byKey[fileKey]?.kind).toBe("text");
    expect(projectFilesState(PROJECT).order).toEqual([fileKey]);
    expect(client.readComparison).toHaveBeenCalledWith(
      PROJECT,
      { effectId: "walk-change" },
      { sessionId: "s1" },
    );
    unmount();
  });

  it("chooses Current before opening a Review row", async () => {
    const client = walkClient("src/main.ts");
    const { unmount } = renderView(client, { sessionId: "s1" });

    await enterWalk(PROJECT, client, "s1");
    await waitFor(() => expect(isWalking(PROJECT)).toBe(true));
    await waitFor(() =>
      expect(screen.getByTestId("file-version-trigger").getAttribute("aria-label"))
        .not.toBe("File version: Current"),
    );

    setFilesStagePaneMode(PROJECT, "review");
    await waitFor(() =>
      expect(screen.getByTestId("files-review-scroll").hidden).toBe(false),
    );
    fireEvent.click(screen.getByTestId("sidebar-scope-picker"));
    fireEvent.click(screen.getByTestId("sidebar-scope-opt-new"));
    await waitFor(() => expect(isWalking(PROJECT)).toBe(false));
    fireEvent.click(await screen.findByTestId("changes-row-file"));

    await waitFor(() =>
      expect(screen.getByTestId("file-version-trigger").getAttribute("aria-label"))
        .toBe("File version: Current"),
    );
    expect(projectFilesState(PROJECT).activeKey).toBe(
      fileBufferKey("r1", "src/main.ts", undefined, "file-src/main.ts"),
    );
    unmount();
  });

  it.each(["text", "oversized", "binary"])("shows a current %s replacement instead of stale deletion marks", async (kind) => {
    const path = "THIRD-PARTY-NOTICES.md";
    const deleted = {
      ...changeFor(path, "deleted-change"),
      tip: { state: "absent" as const },
      effects: [{
        ...changeFor(path, "deleted-change").effects[0]!,
        op: "delete" as const,
      }],
    };
    const getProjectSource = vi.fn(async () => ({
      workspace_id: "workspace-1",
      workspace_kind: "project" as const,
      file_id: deleted.file_id,
      version_id: "recreated-version",
      path,
      content: kind === "text" ? "recreated\n" : "",
      over_limit: kind === "oversized",
      writable: true,
      binary: kind === "binary",
      size_bytes: 10,
      sha256: kind === "text" ? "recreated-sha" : undefined,
      encoding: kind === "binary" ? undefined : "utf-8" as const,
    }));
    const client = stubFilesClient({
      browseProjectSource: vi.fn(async (_projectId, input) => ({
        workspace_id: "workspace-1",
        root_id: input.rootId,
        dir: input.dir,
        watch_complete: true,
        entries: input.dir === "." ? [{ name: path, is_dir: false }] : [],
      })),
      listProjectSourceWalk: vi.fn(async () => ({
        files: [deleted],
        commit_available: false,
        git_changes: [],
        commands: [],
        turns: [],
      })),
      getProjectSource,
    });

    const { unmount } = renderView(client);
    await waitFor(() => {
      expect(isDeletedInScope(PROJECT, "r1", path)).toBe(true);
    });
    fireEvent.click(await findFileRow(path));

    await waitFor(() => expect(getProjectSource).toHaveBeenCalled());
    expect(screen.queryByTestId("review-diff")).toBeNull();
    expect(await screen.findByTestId(kind === "text" ? "files-editor" : "files-info-card")).toBeTruthy();
    const active = projectFilesState(PROJECT).activeKey;
    expect(active && projectFilesState(PROJECT).byKey[active]?.kind).toBe(kind === "text" ? "text" : "info");
    expect(screen.getByTestId("files-pane-files").getAttribute("aria-pressed"))
      .toBe("true");
    expect((await findFileRow(path)).getAttribute("data-deleted")).toBeNull();
    const tab = screen.getByTestId("files-tab");
    expect(tab.querySelector('.den-files-tab__name[data-file-change="deleted"]')).toBeNull();
    expect(tab.querySelector('.den-files-tab__name[data-file-change="changed"]')).toBeTruthy();
    unmount();
  });

  it("shows retained history when the path is still deleted", async () => {
    const path = "deleted.ts";
    const deleted = {
      ...changeFor(path, "deleted-change"),
      tip: { state: "absent" as const },
      effects: [{
        ...changeFor(path, "deleted-change").effects[0]!,
        op: "delete" as const,
      }],
    };
    const client = stubFilesClient({
      listProjectSourceWalk: vi.fn(async () => ({
        files: [deleted],
        commit_available: false,
        git_changes: [], commands: [], turns: [],
      })),
      getProjectSource: vi.fn(async () => {
        throw new LycaonApiError("Source file not found", 404, "source_not_found");
      }),
      browseProjectSource: vi.fn(async (_projectId, input) => {
        if (input.dir !== ".") {
          throw new LycaonApiError("Source path not found", 404, "source_not_found");
        }
        return {
          workspace_id: "workspace-1",
          root_id: input.rootId,
          dir: input.dir,
          watch_complete: true,
          entries: [],
        };
      }),
      listProjectSourceVersions: vi.fn(async () => ({
        file_id: deleted.file_id,
        current: { state: "absent" as const },
        versions: [],
        commits: [],
        arrivals: [],
        git_history_state: "available" as const,
        tracked_since: null,
      })),
    });

    setFilesStagePaneMode(PROJECT, "review");
    const { unmount } = renderView(client);
    fireEvent.click(await screen.findByTestId("changes-row-file"));

    expect(await screen.findByTestId("file-current-absent")).toBeTruthy();
    expect(screen.getByTestId("files-tab").querySelector('.den-files-tab__name[data-file-change="deleted"]')).toBeTruthy();
    expect(screen.getByTestId("files-editor-mode").textContent).toContain(
      "Deleted",
    );
    unmount();
  });

  it.each([
    ["not_captured", "Content was not captured for this version."],
    ["unavailable", "Content for this version is unavailable."],
    ["binary", "This version is binary and can’t be shown as text."],
  ] as const)("keeps a deleted path addressable when its contents are %s", async (availability, message) => {
    openFilesBuffer(PROJECT, { rootId: "r1", rootLabel: "main", path: "gone.ts", intent: "permanent" });
    const { unmount } = renderView({ getProjectSource: vi.fn(async () => ({
      workspace_id: "workspace-1", workspace_kind: "project" as const, file_id: "deleted-file", version_id: "deleted-version",
      root_id: "r1", path: "gone.ts", content: "", binary: false, over_limit: false, size_bytes: 0, writable: false,
      deleted: deletedSourceFixture({ deleted_at: "2026-09-12T10:00:00Z", previous: { state: "content" as const, availability, content: "", size_bytes: 100 } }),
    })) });
    expect(await screen.findByText(message)).toBeTruthy();
    expect(screen.getByTestId("files-tab").querySelector('.den-files-tab__name[data-file-change="deleted"]')).toBeTruthy();
    unmount();
  });

  it("opens a deleted chat path read-only and follows a recreated path on the next open", async () => {
    let recreated = false;
    const getProjectSource = vi.fn(async () => ({
      workspace_id: "workspace-1", workspace_kind: "project" as const,
      file_id: recreated ? "replacement-file" : "deleted-file", version_id: recreated ? "replacement-version" : "deleted-version",
      root_id: "r1", path: "gone.ts", content: recreated ? "replacement contents\n" : "",
      binary: false, over_limit: false, size_bytes: recreated ? 21 : 0,
      writable: recreated, encoding: recreated ? "utf-8" as const : undefined, sha256: recreated ? "replacement-sha" : undefined,
      deleted: recreated ? undefined : deletedSourceFixture({
        deleted_at: "2026-09-12T10:00:00Z",
        previous: { version_id: "before-delete", state: "content" as const, availability: "available" as const,
          content: "retained contents\nsecond line\n", size_bytes: 30 },
      }),
    }));
    const baseClient = getLycaonClient()!;
    const openDocument = vi.fn((...args: Parameters<typeof baseClient.openEditorDocument>) => baseClient.openEditorDocument(...args));
    const { unmount } = renderView({ getProjectSource, openEditorDocument: openDocument });
    const open = () => openSourceInFilesStage({
      request: { projectId: PROJECT, rootId: "r1", path: "gone.ts", absolutePath: "/repo/gone.ts", intent: "permanent", line: 2 },
      rootLabelFor: () => "main", navigate: () => {},
    });
    open();
    const deleted = await screen.findByTestId("file-deletion-document");
    await waitFor(() => expect(deleted.querySelector(".cm-content")?.textContent).toContain("retained contents"));
    expect(deleted.querySelector(".cm-content")?.getAttribute("contenteditable")).toBe("false");
    const retainedView = EditorView.findFromDOM(deleted.querySelector(".cm-content")!);
    await waitFor(() => expect(retainedView?.state.selection.main.head).toBe("retained contents\n".length));
    expect(screen.getByTestId("files-tab").querySelector('.den-files-tab__name[data-file-change="deleted"]')).toBeTruthy();
    expect(openDocument).not.toHaveBeenCalled();
    expect(filesBufferBase(projectFilesState(PROJECT).byKey[projectFilesState(PROJECT).activeKey!]!)).toBe("");
    recreated = true;
    open();
    await waitFor(() => expect(projectFilesState(PROJECT).byKey[projectFilesState(PROJECT).activeKey!]!.fileId).toBe("replacement-file"));
    expect(getProjectSource).toHaveBeenCalledTimes(3);
    expect(screen.queryByTestId("file-deletion-document")).toBeNull();
    expect(screen.getByTestId("files-tab").querySelector('.den-files-tab__name[data-file-change="deleted"]')).toBeNull();
    unmount();
  });

  it("preserves a draft edited while a path recheck is in flight", async () => {
    const source: ProjectSourceReadResponse = {
      workspace_id: "workspace-1", workspace_kind: "project", file_id: "file-gone", version_id: "v1",
      root_id: "r1", path: "gone.ts", content: "working contents\n", binary: false, over_limit: false,
      size_bytes: 17, writable: true, encoding: "utf-8", sha256: "working-sha",
    };
    const provisional = openFilesBuffer(PROJECT, { rootId: "r1", rootLabel: "main", path: "gone.ts", intent: "permanent" });
    const key = applyFilesBufferLoad(PROJECT, provisional, source);
    let finishRead!: (source: ProjectSourceReadResponse) => void;
    const pending = new Promise<ProjectSourceReadResponse>((resolve) => { finishRead = resolve; });
    const getProjectSource = vi.fn(() => pending);
    const { unmount } = renderView({ getProjectSource });
    openSourceInFilesStage({
      request: { projectId: PROJECT, rootId: "r1", path: "gone.ts", absolutePath: "/repo/gone.ts", intent: "permanent" },
      rootLabelFor: () => "main", navigate: () => {},
    });
    await waitFor(() => expect(getProjectSource).toHaveBeenCalledOnce());
    applyFilesBufferDraft(PROJECT, key, "unsaved edits\n");
    finishRead({ ...source, content: "", sha256: "", writable: false,
      deleted: deletedSourceFixture({ deleted_at: "2026-09-12T10:00:00Z", previous: {
        state: "content", availability: "available", content: source.content, size_bytes: source.size_bytes,
      } }),
    });
    await waitFor(() => expect(projectFilesState(PROJECT).byKey[key]?.loading).toBe(false));
    expect(projectFilesState(PROJECT).byKey[key]).toMatchObject({ dirty: true });
    expect(filesBufferText(required(projectFilesState(PROJECT).byKey[key]))).toEqual("unsaved edits\n");
    expect(screen.queryByTestId("file-deletion-document")).toBeNull();
    unmount();
  });

  it("leaves a tab this Walk step never touched addressed as it was", async () => {
    const key = openFilesBuffer(PROJECT, {
      rootId: "r1",
      rootLabel: "repo",
      fileId: "file-src/main.ts",
      path: "src/main.ts",
      intent: "permanent",
    });
    const client = walkClient("src/other.ts");
    const { unmount } = renderView(client, { sessionId: "s1" });

    await enterWalk(PROJECT, client, "s1");
    await waitFor(() => expect(isWalking(PROJECT)).toBe(true));
    expect(projectFilesState(PROJECT).byKey[key]?.kind).toBe("text");
    expect(projectFilesState(PROJECT).order).toContain(key);
    leaveWalk(PROJECT);
    unmount();
  });

  it("opens Walk steps as durable tabs and reactivates tabs already open", async () => {
    const client = stubFilesClient({
      listProjectSourceWalk: vi.fn(async () => ({
        files: [
          changeFor("src/a.ts", "walk-a"),
          changeFor("src/b.ts", "walk-b"),
        ].map((file, i) => ({
          ...file,
          effects: [
            {
              ...file.effects[0]!,
              tool_call_id: i === 0 ? "t1" : "t2",
              tool_name: "write",
              ts:
                i === 0
                  ? "2026-08-09T10:01:00Z"
                  : "2026-08-09T10:02:00Z",
            },
          ],
        })),
        commit_available: false,
        git_changes: [], commands: [], turns: [],
      })),
      readComparison: vi.fn(async () => ({
        in_range: true,
        before: { state: "content", size_bytes: 4, availability: "available", content: "old\n" },
        after: { state: "content", size_bytes: 4, availability: "available", content: "new\n" },
        location_changed: false,
      })),
    });
    const firstKey = openFilesBuffer(PROJECT, {
      rootId: "r1",
      rootLabel: "repo",
      fileId: "file-src/a.ts",
      path: "src/a.ts",
      intent: "permanent",
    });
    const unrelatedKey = openFilesBuffer(PROJECT, {
      rootId: "r1",
      rootLabel: "repo",
      fileId: "file-src/unrelated.ts",
      path: "src/unrelated.ts",
      intent: "permanent",
    });
    expect(projectFilesState(PROJECT).activeKey).toBe(unrelatedKey);

    const { unmount } = renderView(client, { sessionId: "s1" });

    await enterWalk(PROJECT, client, "s1");
    const secondKey = fileBufferKey("r1", "src/b.ts", undefined, "file-src/b.ts");
    await waitFor(() => {
      expect(projectFilesState(PROJECT).activeKey).toBe(firstKey);
    });
    expect(projectFilesState(PROJECT).byKey[firstKey]?.kind).toBe("text");
    expect(projectFilesState(PROJECT).order).toEqual([firstKey, unrelatedKey]);

    setWalkAt(PROJECT, 1);
    await waitFor(() => expect(walkState(PROJECT).at).toBe(1));
    await waitFor(() => {
      expect(projectFilesState(PROJECT).activeKey).toBe(secondKey);
    });
    expect(projectFilesState(PROJECT).byKey[secondKey]?.kind).toBe("text");
    expect(projectFilesState(PROJECT).order).toEqual([
      firstKey,
      unrelatedKey,
      secondKey,
    ]);

    setWalkAt(PROJECT, 0);
    await waitFor(() => {
      expect(projectFilesState(PROJECT).activeKey).toBe(firstKey);
    });
    expect(projectFilesState(PROJECT).order).toEqual([
      firstKey,
      unrelatedKey,
      secondKey,
    ]);
    openFilesBuffer(PROJECT, {
      rootId: "r1", rootLabel: "repo", fileId: "file-src/unrelated.ts",
      path: "src/unrelated.ts", intent: "permanent",
    });
    expect(projectFilesState(PROJECT).activeKey).toBe(unrelatedKey);
    setWalkAt(PROJECT, 0);
    await waitFor(() => expect(projectFilesState(PROJECT).activeKey).toBe(firstKey));
    unmount();
  });

  it("opens a group step as a page tab whose links open file versions beside it", async () => {
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(400);
    const rewritten = (path: string, id: string, ordinal: number) => ({
      ...changeFor(path, id),
      effects: [{
        ...changeFor(path, id).effects[0]!,
        origin: "external" as const,
        cause: "filesystem_reconcile",
        turn: 0,
        ordinal,
        git_change_id: "t1",
      }],
    });
    const client = stubFilesClient({
      listProjectSourceWalk: vi.fn(async () => ({
        files: [
          changeFor("src/a.ts", "walk-a"),
          rewritten("src/b.ts", "cg-b", 3),
          rewritten("src/c.ts", "cg-c", 4),
        ],
        commit_available: false,
        git_changes: [{
          session_id: "", turn: 0, tool_call_id: "", tool_name: "",
          id: "t1", root_id: "r1", kind: "checkout", from_ref: "main", to_ref: "feature-x",
          from_commit: "aaaaaaaaaaaa", to_commit: "bbbbbbbbbbbb", ordinal: 2,
          ts: "2026-08-09T10:01:30Z",
        }],
        commands: [], turns: [],
      })),
      getProjectSourceGitReview: vi.fn(async () => gitReview([])),
      readComparison: vi.fn(async () => ({
        in_range: true,
        before: { state: "content", size_bytes: 4, availability: "available", content: "old\n" },
        after: { state: "content", size_bytes: 4, availability: "available", content: "new\n" },
        location_changed: false,
      })),
    });
    const { unmount } = renderView(client, { sessionId: "s1" });

    await waitFor(() => expect(screen.getByTestId("project-files-stage-boundary").dataset.boot).toBe("ready"));
    await enterWalk(PROJECT, client, "s1");
    await waitFor(() => expect(isWalking(PROJECT)).toBe(true));
    expect(walkState(PROJECT).walk.steps.map((step) => step.key)).toEqual(["walk-a", "git:t1"]);

    setWalkAt(PROJECT, 1);
    await waitFor(() => expect(walkState(PROJECT).at).toBe(1));
    const pageKey = "walk:git:t1";
    await waitFor(() => expect(projectFilesState(PROJECT).activeKey).toBe(pageKey));
    const page = projectFilesState(PROJECT).byKey[pageKey];
    expect(page?.kind).toBe("walk");
    expect(page?.name).toBe("Git checkout · main → feature-x");
    await waitFor(() => expect(screen.getByTestId("walk-page-title").textContent).toContain("Record the changes"));
    expect(screen.getByTestId("walk-page-position").textContent).toContain("Step 2 of 2");
    fireEvent.click(screen.getByText("2 working-tree files changed when this movement was observed"));
    const rows = await screen.findAllByRole("button", { name: /Open observed version/ });
    expect(rows).toHaveLength(2);
    expect(rows[0]!.textContent).toContain("src/b.ts");
    expect(rows[1]!.textContent).toContain("src/c.ts");

    fireEvent.click(rows[1]!);
    const fileKey = fileBufferKey("r1", "src/c.ts", undefined, "file-src/c.ts");
    await waitFor(() => expect(projectFilesState(PROJECT).activeKey).toBe(fileKey));
    expect(client.readComparison).toHaveBeenCalledWith(
      PROJECT,
      { effectId: "cg-c" },
      { sessionId: "s1" },
    );
    // The page stays open beside the file and the walk stays on its step.
    expect(projectFilesState(PROJECT).order).toContain(pageKey);
    expect(isWalking(PROJECT)).toBe(true);
    expect(walkState(PROJECT).at).toBe(1);
    await waitFor(() =>
      expect(screen.getByTestId("file-version-trigger").getAttribute("aria-label"))
        .not.toBe("File version: Current"),
    );
    unmount();
  });

  it("renders an immutable preview in the version editor", async () => {
    const key = openFilesBuffer(PROJECT, {
      intent: "transient",
      rootId: "r1",
      rootLabel: "repo",
      path: "src/main.ts",
      kind: "diff",
      jobId: previewDiffBufferJobId("src/main.ts"),
      diffPreview: {
        version: fileVersionFromSnapshot({
          rootId: "r1", path: "src/main.ts", before: "a", after: "b",
        }),
      },
    });
    const { unmount } = renderView({
      listProjectSourceWalk: vi.fn(async () => ({
        files: [changeFor("src/main.ts", "commit-change")],
        commit_available: true,
        git_changes: [], commands: [], turns: [],
      })),
    });

    setSidebarScope(PROJECT, { kind: "commit" });

    await waitFor(() => {
      expect(projectFilesState(PROJECT).order).toEqual([key]);
    });
    expect(projectFilesState(PROJECT).byKey[key]?.diffPreview?.version).toMatchObject({ source: { kind: "text", before: "a", after: "b" } });
    const host = await screen.findByTestId("file-version-file");
    await waitFor(() => expect(host.querySelector(".cm-den-reader-add")?.textContent).toBe("b"));
    expect(host.querySelector(".cm-den-reader-delete")?.textContent).toBe("a");
    expect(screen.getByTestId("files-editor-mode").textContent).toContain(
      "Preview",
    );
    expect(screen.queryByTestId("file-version-exit")).toBeNull();
    expect(screen.queryByTestId("file-version-trigger")).toBeNull();
    unmount();
  });
});
