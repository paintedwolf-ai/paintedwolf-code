import { required } from "../../test/at.ts";
import "../../test/document-outbox-fixture.ts";
import { filesBufferBase, filesBufferText } from "../documents/project-files-buffers.ts";
import { stubFilesClient } from "../../test/source-client-fixture.ts";
import { PROJECT, ROOTS, loadedBuffer, resetProjectFilesViewTest, sourceWorkspace, PROJECT_SOURCE_IDENTITY, deferred, waitForFilesStageReady } from "./project-files-view-test-harness.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { BackendTransportError } from "../../platform/connection/request-connectivity.ts";
import type { ProjectRoot } from "../../api/types.ts";
import type { ResidentPresence } from "../../ui/resident-surfaces.ts";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import { ProjectFilesView } from "./ProjectFilesView.tsx";
import { suspendFileDocument } from "../documents/files-residency.ts";
import { PROJECT_WORKSPACE_LOADING_HINT } from "../../components/shell/ProjectLoadingStage.tsx";
import { editorReplica } from "../documents/editor-document.ts";
import { createAppStore } from "../../store/app-state.ts";
import { applyFilesBufferDraft, openFilesBuffer, setFilesActiveBuffer } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { fileBufferKey } from "./project-files-model.ts";
import { findFileRow } from "../../test/files-tree-queries.ts";

describe("ProjectFilesView stage chrome", () => {
  beforeEach(resetProjectFilesViewTest);
  afterEach(() => vi.useRealTimers());

  it("leaves an idle project's suspended selection cold until the stage returns", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const buffer = loadedBuffer();
    const client = getLycaonClient()!;
    const [presence, setPresence] = createSignal<ResidentPresence>("active");
    render(() => <ResidentPresenceProvider presence={presence()}>
      <ProjectFilesView projectId={PROJECT} appStore={appStore} roots={ROOTS} client={client} />
    </ResidentPresenceProvider>);
    const current = () => required(Object.values(projectFilesState(PROJECT).byKey).find(value => value.path === buffer.path));
    await waitFor(() => expect(editorReplica(current().documentId)).toBeDefined());
    setPresence("idle");
    expect(await suspendFileDocument(PROJECT, buffer.rootId, buffer.path, current().documentId ?? undefined)).toBe(true);
    await new Promise(resolve => setTimeout(resolve, 20));
    expect(current().content.state).toBe("suspended");
    expect(current().loading).toBe(false);
    expect(editorReplica(current().documentId)).toBeUndefined();
    setPresence("active");
    await waitFor(() => expect(current().content.state).toBe("document"));
    expect(editorReplica(current().documentId)).toBeDefined();
  });

  it("does not readmit an evicted neighbour until navigation renews its demand", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const first = loadedBuffer({ path: "first.ts" });
    const second = loadedBuffer({ path: "second.ts" });
    render(() => <ProjectFilesView projectId={PROJECT} appStore={appStore} roots={ROOTS} client={getLycaonClient()!} />);
    const current = (path: string) => required(Object.values(projectFilesState(PROJECT).byKey).find(buffer => buffer.path === path));
    await waitFor(() => {
      expect(editorReplica(current(first.path).documentId)).toBeDefined();
      expect(editorReplica(current(second.path).documentId)).toBeDefined();
    });
    await waitForFilesStageReady();
    expect(await suspendFileDocument(PROJECT, first.rootId, first.path, current(first.path).documentId ?? undefined)).toBe(true);
    await new Promise(resolve => setTimeout(resolve, 20));
    expect(current(first.path)).toMatchObject({ content: { state: "suspended" }, loading: false });
    setFilesActiveBuffer(PROJECT, current(first.path).key);
    await waitFor(() => expect(current(first.path).content.state).toBe("document"));
  });

  it("keeps a stable loading surface until workspace and tree boot settle", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const base = getLycaonClient()!;
    let resolveWorkspace!: (value: ReturnType<typeof sourceWorkspace>) => void;
    const workspace = new Promise<ReturnType<typeof sourceWorkspace>>((resolve) => {
      resolveWorkspace = resolve;
    });
    const client = stubFilesClient({
      ...base,
      getSourceWorkspace: vi.fn(() => workspace),
    });
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        projectName="Nomos"
        appStore={appStore}
        roots={[]}
        client={client}
      />
    ));

    const boundary = screen.getByTestId("project-files-stage-boundary");
    expect(boundary.getAttribute("data-ready")).toBe("false");
    const hold = screen.getByTestId("project-loading-stage");
    expect(hold.textContent).toContain("Nomos");
    expect(hold.textContent).toContain(PROJECT_WORKSPACE_LOADING_HINT);

    resolveWorkspace(sourceWorkspace("workspace-ready", []));
    await waitFor(() => {
      expect(boundary.getAttribute("data-ready")).toBe("true");
    });
  });

  it("confirms the physical workspace before browsing from cached state", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const base = getLycaonClient()!;
    const workspace = deferred<ReturnType<typeof sourceWorkspace>>();
    const browseProjectSource = vi.fn(
      async (_projectId: string, input: { rootId: string; dir: string }) => ({
        workspace_id: "workspace-current",
        root_id: input.rootId,
        dir: input.dir,
        watch_complete: true,
        entries: [{ name: "current.ts", is_dir: false }],
      }),
    );
    const client = stubFilesClient({
      ...base,
      getSourceWorkspace: vi.fn(() => workspace.promise),
      browseProjectSource,
    });

    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={client}
      />
    ));

    await waitFor(() =>
      expect(client.getSourceWorkspace).toHaveBeenCalledOnce()
    );
    expect(browseProjectSource).not.toHaveBeenCalled();

    workspace.resolve(sourceWorkspace("workspace-current"));
    await waitFor(() => expect(browseProjectSource).toHaveBeenCalledOnce());
    expect(await findFileRow("current.ts")).toBeTruthy();
  });

  it("re-resolves roots when a source response reports a newer workspace", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const base = getLycaonClient()!;
    const getSourceWorkspace = vi.fn()
      .mockResolvedValueOnce(sourceWorkspace("workspace-retired"))
      .mockResolvedValueOnce(sourceWorkspace("workspace-current"));
    const browseProjectSource = vi.fn(async (_projectId: string, input: {
      rootId: string;
      dir: string;
    }) => ({
      workspace_id: "workspace-current",
      root_id: input.rootId,
      dir: input.dir,
      watch_complete: true,
      entries: [{ name: "current.ts", is_dir: false }],
    }));
    const client = stubFilesClient({
      ...base,
      getSourceWorkspace,
      browseProjectSource,
    });

    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={client}
      />
    ));

    expect(await findFileRow("current.ts")).toBeTruthy();
    expect(getSourceWorkspace).toHaveBeenCalledTimes(2);
    expect(browseProjectSource.mock.calls.length).toBeGreaterThanOrEqual(2);
  });

  it("waits for workspace resolution before loading an existing buffer", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const key = openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "retained.ts",
    });
    const workspace = deferred<ReturnType<typeof sourceWorkspace>>();
    const base = getLycaonClient()!;
    const getProjectSource = vi.fn(async (...args: Parameters<LycaonClient["getProjectSource"]>) => ({
      ...await base.getProjectSource(...args),
      file_id: "file-retained",
      version_id: "version-retained",
    }));
    const client = stubFilesClient({
      ...base,
      getSourceWorkspace: vi.fn(() => workspace.promise),
      getProjectSource,
    });

    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={client}
      />
    ));

    await Promise.resolve();
    expect(getProjectSource).not.toHaveBeenCalled();
    expect(projectFilesState(PROJECT).order).toContain(key);

    workspace.resolve(sourceWorkspace());
    await waitFor(() => expect(getProjectSource).toHaveBeenCalledOnce());
    const resolvedKey = fileBufferKey("r1", "retained.ts", undefined, "file-retained");
    await waitFor(() => {
      expect(projectFilesState(PROJECT).order).toContain(resolvedKey);
      expect(projectFilesState(PROJECT).byKey[resolvedKey]?.loading).toBe(false);
      expect(projectFilesState(PROJECT).byKey[resolvedKey]?.loadError).toBeNull();
      expect(filesBufferBase(projectFilesState(PROJECT).byKey[resolvedKey]!)).toBe("placeholder");
    });
  });

  it("holds and dims the editor while a selected tab loads", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const first = loadedBuffer({ path: "first.ts", content: "first" });
    const nextSource = deferred<
      Awaited<ReturnType<LycaonClient["getProjectSource"]>>
    >();
    const base = getLycaonClient()!;
    const client = stubFilesClient({
      ...base,
      getProjectSource: vi.fn(
        (_projectId: string, path: string) =>
          path === "second.ts"
            ? nextSource.promise
            : base.getProjectSource(_projectId, path),
      ),
    });
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={client}
      />
    ));
    await waitForFilesStageReady();
    await screen.findByTestId("files-editor");

    const second = openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      path: "second.ts",
    });
    setFilesActiveBuffer(PROJECT, first.key);
    setFilesActiveBuffer(PROJECT, second);

    const pane = screen.getByTestId("files-pane-presentation");
    expect(projectFilesState(PROJECT).activeKey).toBe(first.key);
    expect(projectFilesState(PROJECT).pendingKey).toBe(second);
    expect(pane.dataset.pending).toBe("true");
    expect(pane.querySelector("[data-retained]")?.getAttribute("data-retained"))
      .toBe("false");
    expect(screen.getByRole("tab", { name: "second.ts" }).getAttribute("aria-busy")).toBe("true");
    await waitFor(() =>
      expect(pane.querySelector("[data-retained]")?.getAttribute("data-retained"))
        .toBe("true"),
    );
    expect((pane.querySelector("[data-retained]") as HTMLElement | null)?.inert)
      .toBe(true);

    nextSource.resolve({
      ...PROJECT_SOURCE_IDENTITY,
      file_id: "file-second",
      version_id: "version-second",
      root_id: "r1",
      path: "second.ts",
      content: "second",
      over_limit: false,
      writable: true,
      binary: false,
      size_bytes: 6,
      sha256: "second-sha",
      encoding: "utf-8",
    });
    const resolvedSecond = fileBufferKey(
      "r1",
      "second.ts",
      undefined,
      "file-second",
    );
    await waitFor(() =>
      expect(projectFilesState(PROJECT).activeKey).toBe(resolvedSecond),
    );
    await waitFor(() => expect(pane.dataset.pending).toBe("false"));
    expect(screen.getByRole("tab", { name: "second.ts" }).getAttribute("aria-busy")).toBe("false");
  });

  it("keeps an unsaved buffer when its attached root changes physical address", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const buffer = loadedBuffer({ path: "retained.ts", content: "saved" });
    applyFilesBufferDraft(PROJECT, buffer.key, "unsaved draft");
    const [roots, setRoots] = createSignal(ROOTS);
    const base = getLycaonClient()!;
    const getSourceWorkspace = vi.fn(async () =>
      sourceWorkspace(
        getSourceWorkspace.mock.calls.length > 1
          ? "workspace-current"
          : "workspace-1",
        roots(),
      ),
    );
    const client = stubFilesClient({
      ...base,
      getSourceWorkspace,
      browseProjectSource: vi.fn(async (_projectId: string, input: { rootId: string; dir: string }) => ({
        workspace_id: getSourceWorkspace.mock.calls.length > 1 ? "workspace-current" : "workspace-1",
        root_id: input.rootId,
        dir: input.dir,
        watch_complete: true,
        entries: [],
      })),
    });

    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={roots()}
        client={client}
      />
    ));
    await waitFor(() => expect(getSourceWorkspace).toHaveBeenCalledOnce());

    setRoots(ROOTS.map((root) => ({ ...root, path: "/moved/repo" })));
    await waitFor(() => expect(getSourceWorkspace).toHaveBeenCalledTimes(2));
    expect(projectFilesState(PROJECT).order).toContain(buffer.key);
    expect(filesBufferText(projectFilesState(PROJECT).byKey[buffer.key]!)).toBe("unsaved draft");
    expect(projectFilesState(PROJECT).byKey[buffer.key]?.dirty).toBe(true);
  });

  it("drops a buffer when its owning root is detached", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const buffer = loadedBuffer({ path: "detached.ts" });
    const [roots, setRoots] = createSignal<ProjectRoot[]>(ROOTS);
    const base = getLycaonClient()!;
    const client = stubFilesClient({
      ...base,
      getSourceWorkspace: vi.fn(async () => sourceWorkspace("workspace-1", roots())),
    });
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={roots()}
        client={client}
      />
    ));
    await waitFor(() => expect(client.getSourceWorkspace).toHaveBeenCalledOnce());
    expect(projectFilesState(PROJECT).order).toContain(buffer.key);

    setRoots([]);
    await waitFor(() => expect(projectFilesState(PROJECT).order).not.toContain(buffer.key));
    expect(projectFilesState(PROJECT).byKey[buffer.key]).toBeUndefined();
  });

  it("retains the tree and revealed overview while revalidating the workspace on return", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const base = getLycaonClient()!;
    const second = deferred<ReturnType<typeof sourceWorkspace>>();
    const getSourceWorkspace = vi.fn()
      .mockResolvedValueOnce(sourceWorkspace("workspace-1"))
      .mockReturnValueOnce(second.promise);
    const client = stubFilesClient({ ...base, getSourceWorkspace });
    const [presence, setPresence] = createSignal<ResidentPresence>("active");
    render(() => (
      <ResidentPresenceProvider presence={presence()}>
        <ProjectFilesView projectId={PROJECT} appStore={appStore} roots={ROOTS} client={client} />
      </ResidentPresenceProvider>
    ));
    await waitForFilesStageReady();
    const tree = screen.getByTestId("files-tree");
    const overview = screen.getByTestId("files-root-summary");
    const reveal = overview.closest<HTMLElement>(".den-stage-boot");
    await waitFor(() => expect(reveal?.dataset.boot).toBe("ready"));
    const before = tree.textContent;
    expect(before).toContain(ROOTS[0]!.label);
    setPresence("idle");
    setPresence("active");
    await waitFor(() => expect(getSourceWorkspace).toHaveBeenCalledTimes(2));
    expect(screen.getByTestId("files-tree")).toBe(tree);
    expect(tree.textContent).toBe(before);
    expect(screen.getByTestId("files-root-summary")).toBe(overview);
    second.resolve(sourceWorkspace("workspace-1", ROOTS.map((root) => ({
      ...root, path: "/repo-refreshed",
    }))));
    await waitFor(() => expect(screen.getByTestId("files-root-summary-path").textContent).toBe("/repo-refreshed"));
    expect(screen.getByTestId("files-tree")).toBe(tree);
    expect(screen.getByTestId("files-root-summary")).toBe(overview);
    expect(reveal?.dataset.boot).toBe("ready");
  });

  const sessionRecord = (id: string, title: string) => ({
    id,
    owner_person_id: "00000000-0000-4000-8000-000000000002",
    project_id: PROJECT,
    workspace_path: "/tmp/project",
    title,
    posture: "build" as const,
    status: "idle" as const,
    created_at: "t",
    activity_at: "t",
    updated_at: "t",
  });

  it("holds the tree quietly, then dims it, until a chat's own worktree is ready", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    appStore.actions.setCurrentSession(sessionRecord("sess-a", "A"));
    const nextWorkspace = deferred<ReturnType<typeof sourceWorkspace>>();
    const nextListing = deferred<Awaited<ReturnType<LycaonClient["browseProjectSource"]>>>();
    const base = getLycaonClient()!;
    const getSourceWorkspace = vi.fn(
      (_projectId: string, sessionId?: string) =>
        sessionId === "sess-a"
          ? Promise.resolve(sourceWorkspace("workspace-a"))
          : nextWorkspace.promise,
    );
    const browseProjectSource = vi.fn(
      async (
        _projectId: string,
        input: { rootId: string; dir: string },
        sessionId?: string,
      ) => {
        if (sessionId !== "sess-b") {
          return {
            workspace_id: "workspace-a",
            root_id: input.rootId,
            dir: input.dir,
            watch_complete: true,
            entries: [{ name: "first.ts", is_dir: false }],
          };
        }
        return nextListing.promise;
      },
    );
    const client = stubFilesClient({
      ...base,
      getSourceWorkspace,
      browseProjectSource,
    });

    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={client}
      />
    ));
    expect(await findFileRow("first.ts")).toBeTruthy();
    await waitForFilesStageReady();
    const tree = screen.getByTestId("files-tree");
    const content = tree.closest<HTMLElement>(
      ".project-files-stage-boundary__content",
    );

    appStore.actions.setCurrentSession(sessionRecord("sess-b", "B"));
    await waitFor(() => expect(getSourceWorkspace).toHaveBeenCalledTimes(2));
    // Whether the chat changes the checkout is not known until the host answers.
    expect(screen.getByTestId("files-tree")).toBe(tree);
    expect(content?.getAttribute("data-retained")).toBe("false");

    nextWorkspace.resolve(sourceWorkspace("workspace-b", ROOTS, true));
    await waitFor(() =>
      expect(content?.getAttribute("data-retained")).toBe("true"),
    );
    expect(content?.inert).toBe(true);
    await waitFor(() => expect(browseProjectSource).toHaveBeenCalledWith(
      PROJECT, expect.anything(), "sess-b",
    ));
    expect(tree.textContent).toContain("first.ts");
    nextListing.resolve({
      workspace_id: "workspace-b",
      root_id: ROOTS[0]!.id,
      dir: ".",
      watch_complete: true,
      entries: [{ name: "second.ts", is_dir: false }],
    });

    expect(await findFileRow("second.ts")).toBeTruthy();
    expect(screen.getByTestId("files-tree")).toBe(tree);
    expect(tree.textContent).not.toContain("first.ts");
    expect(content?.getAttribute("data-retained")).toBe("false");
    expect(content?.inert ?? false).toBe(false);
  });

  it("keeps everything Files shows when another chat resolves to the same project checkout", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    appStore.actions.setCurrentSession(sessionRecord("sess-a", "A"));
    const base = getLycaonClient()!;
    const getSourceWorkspace = vi.fn(async () => sourceWorkspace("workspace-shared"));
    const browseProjectSource = vi.fn(
      async (_projectId: string, input: { rootId: string; dir: string }) => ({
        workspace_id: "workspace-shared",
        root_id: input.rootId,
        dir: input.dir,
        watch_complete: true,
        entries: [{ name: "shared.ts", is_dir: false }],
      }),
    );
    const client = stubFilesClient({ ...base, getSourceWorkspace, browseProjectSource });
    const createSourceView = vi.spyOn(client, "createSourceView");

    render(() => (
      <ProjectFilesView projectId={PROJECT} appStore={appStore} roots={ROOTS} client={client} />
    ));
    expect(await findFileRow("shared.ts")).toBeTruthy();
    await waitForFilesStageReady();
    const tree = screen.getByTestId("files-tree");
    const content = tree.closest<HTMLElement>(".project-files-stage-boundary__content");
    const listings = browseProjectSource.mock.calls.length;
    const openedViews = createSourceView.mock.calls.length;

    appStore.actions.setCurrentSession(sessionRecord("sess-b", "B"));
    await waitFor(() => expect(getSourceWorkspace).toHaveBeenCalledTimes(2));
    await waitForFilesStageReady();

    expect(screen.getByTestId("files-tree")).toBe(tree);
    expect(content?.getAttribute("data-retained")).toBe("false");
    expect(content?.inert ?? false).toBe(false);
    expect(browseProjectSource).toHaveBeenCalledTimes(listings);
    expect(createSourceView).toHaveBeenCalledTimes(openedViews);
    expect(tree.textContent).toContain("shared.ts");
  });

  it("stops workspace retries while Files is idle", async () => {
    vi.useFakeTimers();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const base = getLycaonClient()!;
    const getSourceWorkspace = vi.fn(async () => {
      throw new Error("unavailable");
    });
    const client = stubFilesClient({ ...base, getSourceWorkspace });
    const [presence, setPresence] = createSignal<ResidentPresence>("idle");

    render(() => (
      <ResidentPresenceProvider presence={presence()}>
        <ProjectFilesView
          projectId={PROJECT}
          appStore={appStore}
          roots={[]}
          client={client}
        />
      </ResidentPresenceProvider>
    ));

    expect(getSourceWorkspace).not.toHaveBeenCalled();
    setPresence("active");
    await Promise.resolve();
    expect(getSourceWorkspace).toHaveBeenCalledTimes(1);
    setPresence("idle");
    await vi.advanceTimersByTimeAsync(15_000);
    expect(getSourceWorkspace).toHaveBeenCalledTimes(1);
  });

  it("defers session workspace details while Files is idle", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    appStore.actions.setCurrentSession({
      id: "sess-idle",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: PROJECT,
      workspace_path: "/tmp/project",
      title: "Idle",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    const base = getLycaonClient()!;
    const getSourceWorkspace = vi.fn(async () => sourceWorkspace("workspace-1", []));
    const client = stubFilesClient({ ...base, getSourceWorkspace });
    const [presence, setPresence] = createSignal<ResidentPresence>("idle");

    render(() => (
      <ResidentPresenceProvider presence={presence()}>
        <ProjectFilesView
          projectId={PROJECT}
          appStore={appStore}
          roots={[]}
          client={client}
        />
      </ResidentPresenceProvider>
    ));

    expect(getSourceWorkspace).not.toHaveBeenCalled();
    setPresence("active");
    await waitFor(() => expect(getSourceWorkspace).toHaveBeenCalledOnce());
  });

  it("revalidates Files before settling a newly focused chat", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    appStore.actions.setCurrentSession({
      id: "sess-a",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: PROJECT,
      workspace_path: "/tmp/a",
      title: "A",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    const base = getLycaonClient()!;
    const getSourceWorkspace = vi.fn(async () => sourceWorkspace("workspace-1", []));
    const client = stubFilesClient({
      ...base,
      getSourceWorkspace,
    });

    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={[]}
        client={client}
      />
    ));

    const boundary = screen.getByTestId("project-files-stage-boundary");
    await waitFor(() => {
      expect(boundary.getAttribute("data-ready")).toBe("true");
    });
    const callsAfterBoot = getSourceWorkspace.mock.calls.length;
    expect(callsAfterBoot).toBe(1);

    appStore.actions.setCurrentSession({
      id: "sess-a",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: PROJECT,
      workspace_path: "/tmp/a",
      title: "A refreshed",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t2",
    });
    await Promise.resolve();
    expect(getSourceWorkspace).toHaveBeenCalledTimes(callsAfterBoot);

    appStore.actions.setCurrentSession({
      id: "sess-b",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: PROJECT,
      workspace_path: "/tmp/a",
      title: "B",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    await waitFor(() => {
      expect(getSourceWorkspace).toHaveBeenCalledTimes(callsAfterBoot + 1);
      expect(boundary.getAttribute("data-ready")).toBe("true");
    });
  });

  it("retries source workspace resolution instead of dying on one failed fetch", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const base = getLycaonClient()!;
    let calls = 0;
    const client = stubFilesClient({
      ...base,
      getSourceWorkspace: vi.fn(async () => {
        calls += 1;
        if (calls === 1) {
          throw new BackendTransportError(new TypeError("Failed to fetch"), "unreachable");
        }
        return sourceWorkspace();
      }),
    });
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={client}
      />
    ));

    await waitFor(
      () => expect(calls).toBeGreaterThanOrEqual(2),
      { timeout: 5000 },
    );
    await waitForFilesStageReady();
    expect(calls).toBe(2);
  });
});
