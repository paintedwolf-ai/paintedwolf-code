import "../../test/document-outbox-fixture.ts";
import { filesBufferText } from "../documents/project-files-buffers.ts";
import { normalizeEolForEditor } from "../../components/source/editor/eol.ts";
import { resetEditorDocumentsForTests } from "../documents/editor-document.ts";
import type { SyncEditorDocumentRequest, SubmitEditorDocumentUpdateRequest } from "../../api/types.ts";
import { DocumentFixture } from "../../test/document-fixture.ts";
import { afterEach, expect, vi } from "vitest";
import { screen, waitFor } from "@solidjs/testing-library";
import type { ProjectRoot, SourceWorkspace } from "../../api/types.ts";
import { takeFilesPresentationLivenessViolations } from "./files-presentation-liveness.ts";
import { resetServedSourcesForTests, servedSource, stubFilesClient } from "../../test/source-client-fixture.ts";
import { overlayRel } from "../../platform/files/overlay-dir.ts";
import { resetEditorPrefsForTests } from "../../settings/editor/editor-prefs.ts";
import { resetScrollMeasureQuietForTests } from "../../platform/scrolling/themed-scrollbars.ts";
import { applyFilesBufferLoad, openFilesBuffer, resetProjectFilesForTests } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { sharedBufferLoadRequests } from "../documents/files-buffer-request-tracking.ts";
import { resetFilesEditorHostForTests } from "../editor/files-editor-host.ts";
import { canEditLoadedSource } from "../../components/source/editor/source-editor-model.ts";
import { resetAppStateSnapshotForTests } from "../../store/app-state-snapshot.ts";
import { resetLayoutStoreForTests } from "../../shell/layout-store.ts";
import { resetShellLayoutBusyForTests } from "../../shell/shell-layout-busy.ts";
import { resetFilesTreeWindowStateForTests } from "../tree/files-tree-window-state.ts";
import { resetFilesTreeViewStateForTests } from "../tree/files-tree-view-state.ts";
import { resetSourceTreeStoreForTests } from "../tree/source-tree-store.ts";
import { resetSourceSymbolsCacheForTests } from "../source/source-symbols-cache.ts";
import { resetFilesStagePaneForTests } from "../review/review-pane.ts";
import {
  resetScopeResolutionForTests,
  resolveScope,
} from "../tree/scope-resolution.ts";
import { resetWalkForTests } from "../walk/walk-store.ts";

const addToChat = vi.hoisted(() => vi.fn());
const openInExternalEditor = vi.hoisted(() =>
  vi.fn(async () => ({ status: "opened" })),
);
const revealInFileManager = vi.hoisted(() =>
  vi.fn(async () => ({ status: "revealed" })),
);
const fileTabDragListeners = vi.hoisted(
  () => new Set<(event: Record<string, unknown>) => void>(),
);

const scopeFixture = vi.hoisted(() => ({ files: [] as unknown[] }));

vi.mock("../../chat/composer/add-to-chat.ts", () => ({
  addToChat: (...args: unknown[]) => addToChat(...args),
}));

vi.mock("../../platform/navigation/external-source.ts", () => ({
  openInExternalEditor: (...args: unknown[]) =>
    openInExternalEditor(...(args as Parameters<typeof openInExternalEditor>)),
}));

vi.mock("../../platform/windows/item-windows.ts", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../platform/windows/item-windows.ts")>();
  return {
    ...actual,
    updateFileTabDropTarget: vi.fn(async () => undefined),
    clearFileTabDropTarget: vi.fn(async () => undefined),
    listenFileTabWindowDrag: vi.fn(
      async (handler: (event: Record<string, unknown>) => void) => {
        fileTabDragListeners.add(handler);
        return () => fileTabDragListeners.delete(handler);
      },
    ),
  };
});

vi.mock("../../platform/files/reveal-in-file-manager.ts", async (original) => ({
  ...await original<typeof import("../../platform/files/reveal-in-file-manager.ts")>(),
  revealInFileManager: (...args: unknown[]) =>
    revealInFileManager(
      ...(args as Parameters<typeof revealInFileManager>),
    ),
}));

const editorFixtures = new Map<string, DocumentFixture>();
export function editorDocumentFixtureFor(projectId: string, rootId: string, path: string): DocumentFixture {
  const id = `document:${rootId}:${path}`;
  let fixture = editorFixtures.get(id);
  if (!fixture) {
    const buffer = Object.values(projectFilesState(projectId).byKey).find((b) => b.rootId === rootId && b.path === path);
    const served = servedSource(projectId, rootId, path);
    const content = served?.content ?? (buffer ? filesBufferText(buffer) : undefined) ?? "placeholder";
    fixture = new DocumentFixture(content, { id, project_id: projectId, root_id: rootId, path, file_id: served?.file_id ?? "" });
    editorFixtures.set(id, fixture);
  }
  return fixture;
}
function editorFixtureById(id: string): DocumentFixture {
  const fixture = editorFixtures.get(id);
  if (!fixture) throw new Error(`Editor fixture not opened: ${id}`);
  return fixture;
}

let defaultClient: ReturnType<typeof stubFilesClient> | undefined;

vi.mock("../../platform/connection/app-connection.ts", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../platform/connection/app-connection.ts")>();
  const { gitStatusReads } = await import("../../test/git-status-fixture.ts");
  return {
    ...actual,
    getLycaonClient: () =>
      defaultClient ??= stubFilesClient({
        getProjectSource: vi.fn(async (_projectId: string, path: string) => ({
          file_id: "", version_id: "",
          workspace_id: "workspace-1",
          workspace_kind: "project" as const,
          root_id: "r1",
          path,
          content: "placeholder",
          over_limit: false,
          writable: true,
          binary: false,
          size_bytes: 11,
          sha256: "abc",
          encoding: "utf-8" as const,
        })),
        getProjectSourceEditorConfig: vi.fn(async (_projectId: string, path: string, rootId: string) => ({ path, root_id: rootId })),
        getSourceWorkspace: vi.fn(async () => sourceWorkspace()),
        openEditorDocument: vi.fn(async (projectId: string, request: { root_id: string; path: string }) =>
          editorDocumentFixtureFor(projectId, request.root_id, request.path).snapshot()),
        createEditorDocumentSnapshot: vi.fn(async (_projectId, id) => editorFixtureById(id).snapshot()),
        replaceEditorDocumentRetention: vi.fn(async () => undefined),
        readEditorDocumentStatuses: vi.fn(async (_projectId: string, ids: string[]) => ({ documents: ids.flatMap(id => editorFixtures.has(id) ? [editorFixtureById(id).snapshot()] : []), missing: ids.filter(id => !editorFixtures.has(id)) })),
        syncEditorDocument: vi.fn(async (projectId: string, id: string, request: SyncEditorDocumentRequest) => editorFixtureById(id).sync(projectId, id, request)),
        submitEditorDocumentUpdate: vi.fn(async (projectId: string, id: string, request: SubmitEditorDocumentUpdateRequest) => editorFixtureById(id).submit(projectId, id, request)),
        publishEditorDocumentPresence: vi.fn(async () => undefined),
        leaveEditorDocument: vi.fn(async () => undefined),
        getProjectSourceRaw: vi.fn(
          async () => new Blob([new Uint8Array([1])], { type: "image/png" }),
        ),
        browseProjectSource: vi.fn(async (_projectId, input) => ({
          workspace_id: "workspace-1",
          root_id: input.rootId,
          dir: input.dir,
          watch_complete: true,
          entries: [],
        })),
        listGitRepos: vi.fn(async () => ({
          active_repo_id: "repo-1",
          repos: [
            {
              repo_id: "repo-1",
              label: "repo",
              root_ids: ["r1"],
              available: true,
              branch: "main",
              head_short: "abc1234",
              upstream: "origin/main",
              ahead: 0,
              behind: 0,
              dirty: true,
              staged_count: 0,
              unstaged_count: 1,
            },
          ],
        })),
        ...gitStatusReads(() => ({
          available: true,
          repo_id: "repo-1",
          root_ids: ["r1"],
          branch: "main",
          head_short: "abc1234",
          upstream: "origin/main",
          ahead: 0,
          behind: 0,
          dirty: true,
          staged_count: 0,
          unstaged_count: 1,
          changed_count: 1,
          files: [
            {
              path: "README.md",
              status: " M",
              root_id: "r1",
              root_relative_path: "README.md",
            },
          ],
        })),
        getProjectSourceIndex: vi.fn(async () => ({
          state: "ready" as const,
          revision: 1,
          refreshing: false,
          coverage: [{ root_id: "r1", state: "ready" as const, discovery_complete: true, refreshing: false, bounded_directories: 0, failed_directories: 0 }],
          file_count: 1,
          roots: [{ root_id: "r1", file_count: 1, resources: [] }],
        })),
        getProjectAgentContext: vi.fn(async (_projectId: string, rootId: string) => ({
          project_id: PROJECT,
          root_id: rootId,
          path: ".",
          instructions_enabled: true,
          skills_enabled: true,
          instructions: [],
          skills: [],
        })),
        listProjectSourceSeen: vi.fn(async () => ({ files: [], more: false })),
        listProjectSourceWalk: vi.fn(async () => ({
          files: scopeFixture.files,
          git_changes: [], commands: [], turns: [],
          commit_available: true,
        })),
        listProjectSourceVersions: vi.fn(async () => ({
          file_id: "file-main",
          current: { state: "content" as const, sha256: "base-sha" },
          versions: [],
          commits: [],
          arrivals: [],
          git_history_state: "not_requested",
        })),
        getProjectSourceComparison: vi.fn(async () => ({
          in_range: false,
          location_changed: false,
        })),
        listProjectSourcePins: vi.fn(async () => ({ pins: [] })),
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
        resolveProjectSourceDefinition: vi.fn(async () => ({
          candidates: [],
          truncated: false,
        })),
      }, (projectId, source) => {
        const id = `document:${source.root_id}:${source.path}`;
        if (!source.writable || source.binary || source.over_limit || !source.encoding || source.deleted || editorFixtures.has(id)) return;
        const normalized = normalizeEolForEditor(source.content);
        editorFixtures.set(id, new DocumentFixture(normalized.text, {
          id, project_id: projectId, workspace_id: source.workspace_id, root_id: source.root_id ?? "r1",
          path: source.path, file_id: source.file_id ?? "", encoding: source.encoding,
          base_content: normalized.text, base_sha256: source.sha256 ?? "",
          eol: normalized.eol, base_eol: normalized.eol, mixed_eol: normalized.mixed, base_mixed_eol: normalized.mixed,
        }));
      }),
  };
});

// Hoisted bindings stay module-private.
export function filesViewTestFakes() {
  return {
    addToChat,
    openInExternalEditor,
    revealInFileManager,
    fileTabDragListeners,
    scopeFixture,
  };
}

export function sourceWorkspace(
  workspaceId = "workspace-1",
  roots: readonly ProjectRoot[] = ROOTS,
  sessionScoped = false,
): SourceWorkspace {
  return {
    workspace_id: workspaceId,
    session_scoped: sessionScoped,
    roots: roots.map((root) => ({
      id: root.id,
      path: root.path,
      watch: { state: "live", recursive: true, unwatched_directories: 0 },
    })),
    inventory: { status: "ready", complete: true, roots_generation: 0, indexed_files: 0 },
  };
}

// Pending file selections require an active read or workspace preparation.
afterEach(() => {
  expect(
    takeFilesPresentationLivenessViolations(),
    "a Files buffer stayed pending with no read in flight — its open path never presents",
  ).toEqual([]);
});

export function resetProjectFilesViewTest() {
  defaultClient = undefined;
  resetServedSourcesForTests();
  resetEditorDocumentsForTests();
  for (const fixture of editorFixtures.values()) fixture.doc.destroy();
  editorFixtures.clear();
  takeFilesPresentationLivenessViolations();
  addToChat.mockReset();
  openInExternalEditor.mockClear();
  revealInFileManager.mockClear();
  fileTabDragListeners.clear();
  scopeFixture.files = [];
  resetProjectFilesForTests();
  sharedBufferLoadRequests.clear();
  resetFilesEditorHostForTests();
  resetEditorPrefsForTests();
  resetShellLayoutBusyForTests();
  resetScrollMeasureQuietForTests();
  resetLayoutStoreForTests();
  resetFilesTreeWindowStateForTests();
  resetFilesTreeViewStateForTests();
  resetSourceTreeStoreForTests();
  resetSourceSymbolsCacheForTests();
  resetAppStateSnapshotForTests();
  resetFilesStagePaneForTests();
  resetScopeResolutionForTests();
  resetWalkForTests();
  document.body.innerHTML = "";
}

export const seedScope = async (
  projectId: string,
  files: ReadonlyArray<{
    rootId: string;
    path: string;
    changeId?: string;
    sha?: string;
    deleted?: boolean;
    op?: "write" | "create" | "delete" | "rename";
  }>,
) =>
  await resolveScope(projectId, stubFilesClient({
    listProjectSourceSeen: vi.fn(async () => ({ files: [], more: false })),
    // The host answers with the baseline it resolved for the request.
    listProjectSourceWalk: vi.fn(async (_projectId: string, request?: { baseline?: string }) => ({
      baseline: request?.baseline ?? "presentation",
      files: files.map((f) => ({
        file_id: `file-${f.path}`,
        root_id: f.rootId,
        path: f.path,
        changed_since_presented: true,
        unpresented_agent_effects: f.changeId ? 1 : 0,
        tip: f.deleted
          ? { state: "absent" }
          : { state: "content", sha256: f.sha ?? "" },
        effects: f.changeId
          ? [
              {
                id: f.changeId,
                project_id: projectId,
                operation_id: `operation-${f.changeId}`,
                file_id: `file-${f.path}`,
                after_version_id: `version-${f.changeId}`,
                workspace_id: "workspace-1",
                workspace_kind: "project" as const,
                root_id: f.rootId,
                path: f.path,
                op: f.deleted ? "delete" : (f.op ?? "write"),
                entry_kind: "file",
                origin: "agent" as const,
                turn: 1,
                ordinal: 1,
                ts: "2026-08-01T00:00:00Z",
                cause: "tool",
                capture_quality: "exact" as const,
              },
            ]
          : [],
      })),
      git_changes: [],
      commands: [],
      turns: [],
      commit_available: false,
    })),
  }), null);

export const PROJECT = "p1";
export const PROJECT_SOURCE_IDENTITY = {
  workspace_id: "workspace-1",
  workspace_kind: "project" as const,
};

export function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((accept, fail) => {
    resolve = accept;
    reject = fail;
  });
  return { promise, resolve, reject };
}

export async function waitForFilesStageReady() {
  await waitFor(() => {
    expect(
      screen.getByTestId("project-files-stage-boundary").dataset.boot,
    ).toBe("ready");
  });
}
export const BLUEPRINT_PATH = overlayRel("blueprints", "test.md");
export const ROOTS: ProjectRoot[] = [
  {
    id: "r1",
    path: "/repo",
    label: "repo",
    is_primary: true,
    added_at: "2026-01-01T00:00:00Z",
    kind: "attached",
  },
];

export function loadedBuffer(args?: {
  path?: string;
  content?: string;
  sha256?: string;
  overLimit?: boolean;
  kind?: "text" | "image" | "info";
  jobId?: string;
}) {
  const path = args?.path ?? "src/main.ts";
  const key = openFilesBuffer(PROJECT, {
    intent: "permanent",
    rootId: "r1",
    rootLabel: "repo",
    path,
    jobId: args?.jobId,
  });
  applyFilesBufferLoad(PROJECT, key, {
    file_id: "",
    version_id: "",
    workspace_id: args?.jobId ? "workspace-worker" : "workspace-1",
    workspace_kind: args?.jobId ? "worker" : "project",
path,
    content: args?.content ?? "line1\nline2\nline3 target\nline4\n",
    over_limit: args?.overLimit ?? false,
    writable: true,
    binary: args?.kind === "image" || args?.kind === "info",
    size_bytes: 100,
    ...(args?.overLimit || args?.kind === "info" || args?.kind === "image"
      ? { mime: args?.kind === "image" ? "image/png" : "application/octet-stream" }
      : {}),
    ...(args?.overLimit || args?.kind
      ? {}
      : { sha256: args?.sha256 ?? "base-sha", encoding: "utf-8" as const }),
  });
  return projectFilesState(PROJECT).byKey[key]!;
}

export function editorProps(buf: ReturnType<typeof loadedBuffer>) {
  return {
    projectId: PROJECT,
    buffer: buf,
    roots: ROOTS,
    saving: false,
    saveError: null,
    conflict: false,
    heldAgentEdit: false,
    updatedNote: false,
    definitionNotice: null,
    observationNotice: null,
    savedFlash: false,
    editable: canEditLoadedSource({
      kind: buf.kind,
      overLimit: buf.overLimit,
      sha256: buf.baseSha256,
      encoding: buf.encoding,
      jobId: buf.jobId,
      writable: buf.writable,
    }),
    version: null,
    versionHistory: {
      status: "ready" as const,
      gitStatus: "ready" as const,
      current: { state: "content" as const, sha256: buf.baseSha256 ?? undefined },
      versions: [],
      commits: [],
      arrivals: [],
      gitHistoryState: "available" as const,
      trackedSince: null,
      nextVersionsCursor: null,
      nextGitCursor: null,
      loadingMore: false,
      error: null,
    },
    restoringVersion: false,
    versionRestoreDisabledReason: null,
    onRestoreVersion: vi.fn(),
    onSelectVersion: vi.fn(),
    onSelectCommit: vi.fn(),
    onRestoreListedVersion: vi.fn(),
    onRestoreListedCommit: vi.fn(),
    listedRestoreReason: () => null,
    onLoadVersionHistory: vi.fn(),
    onLoadEarlierVersions: vi.fn(),
    cursor: { line: 1, col: 1 },
    onCursor: vi.fn(),
    onSave: vi.fn(),
    onDiscard: vi.fn(),
    onSelectCurrent: vi.fn(),
    onReload: vi.fn(),
    onMerge: vi.fn(),
    onInnerLayerChange: vi.fn(),
    onRevealSegment: vi.fn(),
    onSymbolJump: vi.fn(),
    onCursorTeleport: vi.fn(),
    definitionPicker: null,
    onDefinitionPickerChange: vi.fn(),
    onGoToDefinitionAt: vi.fn(),
    onJumpDefinitionCandidate: vi.fn(),
  };
}
