// @vitest-environment jsdom
import "../../test/document-outbox-fixture.ts";
import { sourceEffectFixture } from "../source/source-effect-fixture.ts";
// Unread targets replace the displayed file after loading.
import { stubFilesClient, type FileClientStubs } from "../../test/source-client-fixture.ts";
import {
  PROJECT,
  ROOTS,
  resetProjectFilesViewTest,
  sourceWorkspace,
  editorDocumentFixtureFor,
  deferred,
  waitForFilesStageReady,
} from "./project-files-view-test-harness.ts";

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError } from "../../api/http.ts";
import type { EditorDocument, SourceWalkEffect, SourceWorkspace } from "../../api/types.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { createAppStore } from "../../store/app-state.ts";
import { setAppStateSnapshot } from "../../store/app-state-snapshot.ts";
import { invokeCommand } from "../../shortcuts/dispatcher.ts";
import { ProjectFilesView } from "./ProjectFilesView.tsx";
import { openSourceInFilesStage } from "./project-files-open.ts";
import { closeFilesBuffer, openFilesBuffer, setFilesActiveBuffer } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { fileBufferKey, type FileBufferKey } from "./project-files-model.ts";
import { selectedFileVersion } from "../history/files-version-selection.ts";
import { takeFilesPresentationLivenessViolations } from "./files-presentation-liveness.ts";
import { currentWalkStep, stepWalk, walkState } from "../walk/walk-store.ts";

const FILES = ["a.ts", "b.ts", "c.ts"] as const;

function sourceRead(path: string) {
  return {
    workspace_id: "workspace-1",
    workspace_kind: "project" as const,
    file_id: `file-${path}`,
    version_id: `version-${path}`,
    root_id: "r1",
    path,
    content: `content of ${path}`,
    over_limit: false,
    writable: true,
    binary: false,
    size_bytes: 10,
    sha256: `sha-${path}`,
    encoding: "utf-8" as const,
  };
}

function readKey(path: string): FileBufferKey {
  return fileBufferKey("r1", path, undefined, `file-${path}`);
}

type ReadGate = Map<string, ReturnType<typeof deferred<ReturnType<typeof sourceRead>>>>;

function listingClient(options: {
  gate?: ReadGate;
  overrides?: FileClientStubs;
} = {}) {
  const base = getLycaonClient()!;
  const reads: string[] = [];
  const read = async (path: string) => {
    reads.push(path);
    const held = options.gate?.get(path);
    return held ? held.promise : sourceRead(path);
  };
  const client = stubFilesClient({
    ...base,
    browseProjectSource: vi.fn(
      async (_projectId: string, input: { rootId: string; dir: string }) => ({
        workspace_id: "workspace-1",
        root_id: input.rootId,
        dir: input.dir,
        watch_complete: true,
        entries: FILES.map((name) => ({ name, is_dir: false })),
      }),
    ),
    getProjectSource: vi.fn(async (_projectId: string, path: string) => read(path)),
    openEditorDocument: vi.fn(async (_projectId: string, request: { path: string }) => {
      const source = await read(request.path);
      return { ...joinedDocument(request.path), workspace_id: source.workspace_id };
    }),
    ...(options.overrides ?? {}),
  });
  return { client, reads };
}

function mount(client: LycaonClient) {
  const appStore = createAppStore();
  appStore.actions.setSidecarStatus("connected");
  appStore.actions.setCurrentSession({
    id: "session-aim",
    project_id: PROJECT,
    current_turn: 1,
  } as never);
  render(() => (
    <ProjectFilesView
      projectId={PROJECT}
      appStore={appStore}
      roots={ROOTS}
      client={client}
    />
  ));
}

async function paint(path: string) {
  const rows = await screen.findAllByTestId("files-tree-file");
  const row = rows.find((entry) => entry.textContent?.includes(path));
  expect(row, `tree row for ${path}`).toBeTruthy();
  fireEvent.click(row!);
  await expectPresented(readKey(path));
  await waitFor(() => {
    const editors = screen.getAllByTestId("files-editor");
    expect(editors.filter((element) => !element.closest('[aria-hidden="true"]'))).toHaveLength(1);
  });
}

async function expectPresented(key: FileBufferKey) {
  await waitFor(() => expect(projectFilesState(PROJECT).activeKey).toBe(key));
  expect(projectFilesState(PROJECT).pendingKey).toBeNull();
  await waitFor(() => {
    const pane = screen.getByTestId("files-pane-presentation");
    expect(pane.dataset.pending).toBe("false");
    expect(document.querySelector(".den-presentation-wait")).toBeNull();
  });
  // Let the deferred liveness check run before reading it.
  await Promise.resolve();
  expect(takeFilesPresentationLivenessViolations()).toEqual([]);
}

function reveal(path: string) {
  openSourceInFilesStage({
    request: {
      projectId: PROJECT,
      rootId: "r1",
      path,
      absolutePath: `/repo/${path}`,
      intent: "permanent",
    },
    rootLabelFor: () => "repo",
    navigate: () => {},
  });
}

describe("ProjectFilesView aim liveness matrix", () => {
  beforeEach(resetProjectFilesViewTest);

  it("tree click presents an unread file behind a painted one", async () => {
    const { client } = listingClient();
    mount(client);
    await paint("a.ts");

    const rows = screen.getAllByTestId("files-tree-file");
    fireEvent.click(rows.find((row) => row.textContent?.includes("b.ts"))!);
    await expectPresented(readKey("b.ts"));
  });

  it("a reveal request presents an unread file behind a painted one", async () => {
    const { client } = listingClient();
    mount(client);
    await paint("a.ts");

    reveal("b.ts");
    await expectPresented(readKey("b.ts"));
  });

  it("jump back reopens and presents a file whose tab was closed", async () => {
    const { client } = listingClient();
    mount(client);
    await paint("a.ts");
    reveal("b.ts");
    await expectPresented(readKey("b.ts"));
    reveal("c.ts");
    await expectPresented(readKey("c.ts"));
    expect(closeFilesBuffer(PROJECT, readKey("b.ts"))).not.toBeNull();

    expect(invokeCommand("files.jumpBack")).toBe("ran");
    await expectPresented(readKey("b.ts"));
  });

  it("selecting a loading tab presents it when its read lands", async () => {
    const gate: ReadGate = new Map([["b.ts", deferred()]]);
    const { client } = listingClient({ gate });
    mount(client);
    await paint("a.ts");

    const provisional = openFilesBuffer(PROJECT, {
      rootId: "r1",
      rootLabel: "repo",
      path: "b.ts",
      intent: "permanent",
    });
    setFilesActiveBuffer(PROJECT, readKey("a.ts"));
    setFilesActiveBuffer(PROJECT, provisional);
    expect(projectFilesState(PROJECT).pendingKey).toBe(provisional);
    await waitFor(() => expect(screen.getByRole("tab", { name: "b.ts" }).getAttribute("aria-busy")).toBe("true"));
    expect(screen.getByTestId("files-pane-presentation").querySelector('[role="status"]')).toBeNull();

    gate.get("b.ts")!.resolve(sourceRead("b.ts"));
    await expectPresented(readKey("b.ts"));
    expect(screen.getByRole("tab", { name: "b.ts" }).getAttribute("aria-busy")).toBe("false");
  });

  it("a hot-exit restore presents the restored active tab", async () => {
    setAppStateSnapshot({
      version: 1,
      recents: [],
      filesHotExit: {
        byProject: {
          [PROJECT]: {
            buffers: [
              { rootId: "r1", path: "a.ts", rootLabel: "repo", kind: "text" },
              { rootId: "r1", path: "b.ts", rootLabel: "repo", kind: "text" },
            ],
            activeIndex: 1,
          },
        },
      },
    });
    const { client } = listingClient();
    mount(client);

    await expectPresented(readKey("b.ts"));
    expect(projectFilesState(PROJECT).order).toHaveLength(2);
  });

  it("an aim held by workspace resolution loads once the workspace settles", async () => {
    const workspace = deferred<SourceWorkspace>();
    const { client, reads } = listingClient({
      overrides: { getSourceWorkspace: vi.fn(() => workspace.promise) },
    });
    mount(client);

    const provisional = openFilesBuffer(PROJECT, {
      rootId: "r1",
      rootLabel: "repo",
      path: "b.ts",
      intent: "permanent",
    });
    await Promise.resolve();
    expect(projectFilesState(PROJECT).byKey[provisional]?.loading).toBe(true);
    expect(reads).toEqual([]);

    workspace.resolve(sourceWorkspace());
    await waitForFilesStageReady();
    await expectPresented(readKey("b.ts"));
    expect(client.getProjectSource).toHaveBeenCalledTimes(1);
    expect(client.openEditorDocument).toHaveBeenCalledTimes(1);
  });

  it("a failed read presents the failure instead of a veil", async () => {
    const gate: ReadGate = new Map([["b.ts", deferred()]]);
    const { client } = listingClient({ gate });
    mount(client);
    await paint("a.ts");

    const provisional = openFilesBuffer(PROJECT, {
      rootId: "r1",
      rootLabel: "repo",
      path: "b.ts",
      intent: "permanent",
    });
    expect(projectFilesState(PROJECT).pendingKey).toBe(provisional);

    gate.get("b.ts")!.reject(new LycaonApiError("read refused", 500, "internal_error"));
    await expectPresented(provisional);
    expect(projectFilesState(PROJECT).byKey[provisional]?.loadError).toBeTruthy();
  });

  it("a Walk step onto an unread file presents its past version without a read", async () => {
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(400);
    const effect = (id: string, path: string, ordinal: number): SourceWalkEffect => sourceEffectFixture({
      id,
      project_id: PROJECT,
      operation_id: `operation-${id}`,
      file_id: `file-${path}`,
      after_version_id: `version-${id}`,
      workspace_kind: "project",
      root_id: "r1",
      path,
      op: "write",
      entry_kind: "file",
      origin: "user",
      turn: 0,
      ordinal,
      cause: "human_edit",
      capture_quality: "exact",
      observed_at: "2026-08-24T12:00:00Z",
    });
    const effects = [effect("effect-a", "a.ts", 1), effect("effect-b", "b.ts", 2)];
    const { client, reads } = listingClient({
      overrides: {
        listProjectSourceWalk: vi.fn(async () => ({
          files: effects.map((entry) => ({
            file_id: entry.file_id,
            root_id: entry.root_id,
            path: entry.path,
            changed_since_presented: false,
            unpresented_agent_effects: 0,
            tip: { state: "content" as const, sha256: `sha-${entry.id}` },
            effects: [entry],
          })),
          commit_available: true,
          git_changes: [], commands: [], turns: [],
          next_cursor: undefined,
        })),
        readComparison: vi.fn(async () => ({
          in_range: true,
          before: {
            state: "content" as const,
            size_bytes: 6,
            availability: "available" as const,
            content: "before",
          },
          after: {
            state: "content" as const,
            size_bytes: 5,
            availability: "available" as const,
            content: "after",
          },
          location_changed: false,
        })),
      },
    });
    mount(client);
    await paint("a.ts");
    fireEvent.click(screen.getByTestId("walk-button"));
    await waitFor(() => expect(walkState(PROJECT).status).toBe("ready"));
    const first = currentWalkStep(PROJECT);
    expect(first?.kind === "effect" && first.effect.path).toBe("a.ts");

    stepWalk(PROJECT, 1);
    const key = readKey("b.ts");
    await waitFor(() => expect(selectedFileVersion(PROJECT, key)).not.toBeNull());
    await expectPresented(key);
    // Historical content needs no working-file read.
    expect(reads).not.toContain("b.ts");
  });
});

describe("ProjectFilesView tab commands follow the aim", () => {
  beforeEach(resetProjectFilesViewTest);

  it("close tab takes the tab the strip selects, not the file still painted", async () => {
    const gate: ReadGate = new Map([["b.ts", deferred()]]);
    const { client } = listingClient({ gate });
    mount(client);
    await paint("a.ts");

    reveal("b.ts");
    await waitFor(() => expect(projectFilesState(PROJECT).pendingKey).not.toBeNull());
    const pending = projectFilesState(PROJECT).pendingKey!;

    expect(invokeCommand("files.closeTab")).toBe("ran");
    expect(projectFilesState(PROJECT).byKey[pending]).toBeUndefined();
    expect(projectFilesState(PROJECT).order).toEqual([readKey("a.ts")]);
    await expectPresented(readKey("a.ts"));
  });

  it("closes one tab per press without waiting for earlier closes to settle", async () => {
    const { client } = listingClient();
    mount(client);
    await paint("a.ts");
    reveal("b.ts");
    await expectPresented(readKey("b.ts"));
    reveal("c.ts");
    await expectPresented(readKey("c.ts"));
    expect(projectFilesState(PROJECT).order).toHaveLength(3);

    for (let press = 0; press < 3; press += 1) {
      expect(invokeCommand("files.closeTab")).toBe("ran");
    }

    expect(projectFilesState(PROJECT).order).toEqual([]);
    expect(projectFilesState(PROJECT).activeKey).toBeNull();
  });

  it("next tab moves on from the tab the strip selects", async () => {
    const gate: ReadGate = new Map([["b.ts", deferred()]]);
    const { client } = listingClient({ gate });
    mount(client);
    await paint("a.ts");
    reveal("c.ts");
    await expectPresented(readKey("c.ts"));
    reveal("b.ts");
    await waitFor(() => expect(projectFilesState(PROJECT).pendingKey).not.toBeNull());

    expect(invokeCommand("files.nextTab")).toBe("ran");
    await expectPresented(readKey("a.ts"));
  });

  it("pin toggle pins the tab the strip selects, not the file still painted", async () => {
    const gate: ReadGate = new Map([["b.ts", deferred()]]);
    const { client } = listingClient({ gate });
    mount(client);
    await paint("a.ts");

    reveal("b.ts");
    await waitFor(() => expect(projectFilesState(PROJECT).pendingKey).not.toBeNull());
    const pending = projectFilesState(PROJECT).pendingKey!;

    expect(invokeCommand("files.togglePin")).toBe("ran");
    expect(projectFilesState(PROJECT).byKey[pending]?.pinned).toBe(true);
    expect(projectFilesState(PROJECT).byKey[readKey("a.ts")]?.pinned).toBe(false);
  });

  it("loads the aim's neighbours behind it after a restore", async () => {
    setAppStateSnapshot({
      version: 1,
      recents: [],
      filesHotExit: {
        byProject: {
          [PROJECT]: {
            buffers: FILES.map((path) => ({ rootId: "r1", path, rootLabel: "repo", kind: "text" as const })),
            activeIndex: 1,
          },
        },
      },
    });
    const { client, reads } = listingClient();
    mount(client);

    await expectPresented(readKey("b.ts"));
    await waitFor(() => expect(reads).toEqual(expect.arrayContaining(["a.ts", "c.ts"])));
    await waitFor(() => {
      const state = projectFilesState(PROJECT);
      expect(state.order.map((key) => state.byKey[key]?.loading)).toEqual([false, false, false]);
    });
  });

  it("does not join a document when its source arrives after the tab closed", async () => {
    const opened = deferred<EditorDocument>();
    const leaveEditorDocument = vi.fn(async () => joinedDocument("b.ts"));
    const { client } = listingClient({
      overrides: {
        openEditorDocument: vi.fn(async (_projectId: string, request: { path: string }) =>
          request.path === "b.ts" ? opened.promise : joinedDocument(request.path)),
        leaveEditorDocument,
      },
    });
    mount(client);
    await paint("a.ts");

    reveal("b.ts");
    await waitFor(() =>
      expect(client.openEditorDocument).toHaveBeenCalledWith(
        PROJECT,
        expect.objectContaining({ path: "b.ts" }),
        expect.anything(),
      ),
    );
    expect(invokeCommand("files.closeTab")).toBe("ran");
    expect(projectFilesState(PROJECT).order).toEqual([readKey("a.ts")]);

    opened.resolve(joinedDocument("b.ts"));
    await opened.promise;
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(leaveEditorDocument).not.toHaveBeenCalledWith(
      PROJECT, "document:r1:b.ts", expect.anything(),
    );
    expect(projectFilesState(PROJECT).order).toEqual([readKey("a.ts")]);
  });

  it("reopens a file while its previous source acquisition is still pending", async () => {
    const opened = deferred<EditorDocument>();
    const { client } = listingClient({
      overrides: {
        openEditorDocument: vi.fn(async (_projectId: string, request: { path: string }) =>
          request.path === "b.ts" ? opened.promise : joinedDocument(request.path)),
      },
    });
    mount(client);
    await paint("a.ts");
    reveal("b.ts");
    await waitFor(() => expect(client.openEditorDocument).toHaveBeenCalledWith(
      PROJECT, expect.objectContaining({ path: "b.ts" }), expect.anything(),
    ));

    expect(invokeCommand("files.closeTab")).toBe("ran");
    reveal("b.ts");
    opened.resolve(joinedDocument("b.ts"));
    await expectPresented(readKey("b.ts"));
    await waitFor(() => expect(projectFilesState(PROJECT).byKey[readKey("b.ts")]?.documentId)
      .toBe("document:r1:b.ts"));
    expect(projectFilesState(PROJECT).byKey[readKey("b.ts")]?.loading).toBe(false);
  });
});

function joinedDocument(path: string): EditorDocument {
  const fixture = editorDocumentFixtureFor(PROJECT, "r1", path);
  if (fixture.text.toString() !== `content of ${path}`) fixture.replace(`content of ${path}`);
  Object.assign(fixture.wire, { file_id: `file-${path}`, base_content: `content of ${path}`, base_sha256: `sha-${path}` });
  return fixture.snapshot();
}
