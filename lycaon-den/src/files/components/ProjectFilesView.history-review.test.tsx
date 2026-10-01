import "../../test/document-outbox-fixture.ts";
import { PROJECT, ROOTS, resetProjectFilesViewTest, PROJECT_SOURCE_IDENTITY, waitForFilesStageReady, deferred } from "./project-files-view-test-harness.ts";
import { afterEach, beforeEach, describe, expect, it, vi, onTestFinished } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { ProjectFilesView } from "./ProjectFilesView.tsx";
import { createAppStore } from "../../store/app-state.ts";
import { applyFilesBufferLoad, openFilesBuffer, resetProjectFilesForTests } from "../documents/project-files-buffers.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { selectedFileVersion, setSelectedFileVersion } from "../history/files-version-selection.ts";
import { invokeCommand } from "../../shortcuts/dispatcher.ts";
import { resetFilesStagePaneForTests } from "../review/review-pane.ts";
import type { SecretScreen, SourceFileVersionsResponse, SourceWalkEffect } from "../../api/types.ts";
import { stubFilesClient } from "../../test/source-client-fixture.ts";
import { EditorView } from "@codemirror/view";

import type { LycaonClient } from "../../api/client.ts";

import { sourceEffectFixture } from "../source/source-effect-fixture.ts";
import { sourceReaderFixture } from "../../test/source-reader-fixture.ts";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import { walkState, currentWalkStep, refreshWalk } from "../walk/walk-store.ts";

import { fileBufferKey } from "./project-files-model.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";

describe("ProjectFilesView stage chrome", () => {
  beforeEach(resetProjectFilesViewTest);
  afterEach(() => vi.useRealTimers());

  it("opens the existing Review lens from the changed-files badge", async () => {
    resetProjectFilesForTests();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    // The badge reads the repo's dirty state through the client.
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={getLycaonClient()!}
      />
    ));

    await waitForFilesStageReady();
    // The summary resolves its repo state after the stage boots.
    fireEvent.click(
      await screen.findByTestId(
        "files-root-summary-working-tree",
        {},
        { timeout: 5000 },
      ),
    );

    const review = screen.getByTestId("files-pane-review");
    expect(review.getAttribute("aria-pressed")).toBe("true");
    expect(review.getAttribute("aria-label")).toBe("Review");
    expect(review.textContent).toBe("Review");
  });

  it("keeps the Files tree mounted when switching to Review", async () => {
    resetProjectFilesForTests();
    resetFilesStagePaneForTests();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
      />
    ));
    const treeFrame = await screen.findByTestId("files-tree-scroll-frame");
    const reviewScroll = screen.getByTestId("files-review-scroll");
    const walkButton = screen.getByTestId("walk-button");
    const tabStrip = screen.getByTestId("files-tab-strip");
    const toolbar = screen.getByTestId("files-editor-toolbar-empty");
    const summary = screen.getByTestId("files-root-summary");
    expect(
      treeFrame.classList.contains("den-files-tree-body__pane--hidden"),
    ).toBe(false);
    fireEvent.click(screen.getByTestId("files-pane-review"));
    expect(screen.getByTestId("files-pane-review").getAttribute("aria-pressed")).toBe(
      "true",
    );
    expect(
      treeFrame.classList.contains("den-files-tree-body__pane--hidden"),
    ).toBe(true);
    expect(screen.getByTestId("walk-button")).toBe(walkButton);
    expect(screen.getByTestId("files-tree-scroll-frame")).toBe(treeFrame);
    expect(screen.getByTestId("files-review-scroll")).toBe(reviewScroll);
    expect(screen.getByTestId("files-tab-strip")).toBe(tabStrip);
    expect(screen.getByTestId("files-editor-toolbar-empty")).toBe(toolbar);
    expect(screen.getByTestId("files-root-summary")).toBe(summary);
    fireEvent.click(screen.getByTestId("files-pane-files"));
    expect(
      treeFrame.classList.contains("den-files-tree-body__pane--hidden"),
    ).toBe(false);
  });

  it("returns a past version to the working file by command and by its exit", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const key = openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      fileId: "file-main",
      path: "src/main.ts",
    });
    applyFilesBufferLoad(PROJECT, key, {
      ...PROJECT_SOURCE_IDENTITY,
      file_id: "file-main",
      version_id: "version-current",
      path: "src/main.ts",
      content: "current\n",
      over_limit: false,
      writable: true,
      binary: false,
      size_bytes: 8,
      sha256: "current-sha",
      encoding: "utf-8",
    });
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={getLycaonClient()}
      />
    ));
    await screen.findByTestId("files-editor");

    const showOld = async () => {
      setSelectedFileVersion(PROJECT, key, {
        versionId: "version-old",
        fileId: "file-main",
        rootId: "r1",
        path: "src/main.ts",
        op: "write",
        ts: "2026-08-26T10:00:00Z",
        beforeAvailability: "absent",
        sha256: "old-sha",
        sizeBytes: 4,
        availability: "available", source: { kind: "text", before: "", after: "old\n" }
      });
      await waitFor(() => {
        expect(screen.getByTestId("file-version-exit")).toBeTruthy();
        expect(screen.getByTestId("files-editor-mode").textContent)
          .toContain("Historical version");
        // Tab labels describe the displayed version's changes.
        const tab = screen.getByRole("tab", { name: "main.ts, Previous version, Added in this version" });
        expect(tab.querySelector(".den-files-tab-kind-icon")?.getAttribute("data-tip")).toBe("Previous version");
      });
    };

    await showOld();
    expect(invokeCommand("files.currentVersion")).toBe("ran");
    await waitFor(() => {
      expect(selectedFileVersion(PROJECT, key)).toBeNull();
      expect(screen.queryByTestId("file-version-exit")).toBeNull();
      expect(document.querySelector(".den-files-editor__status")?.getAttribute("data-editor-identity"))
        .toBe("editing");
      expect(screen.getByRole("tab", { name: "main.ts" }).querySelector(".den-files-tab-kind-icon")).toBeNull();
    });

    await showOld();
    fireEvent.click(screen.getByTestId("file-version-exit"));
    await waitFor(() => {
      expect(selectedFileVersion(PROJECT, key)).toBeNull();
      expect(screen.queryByTestId("file-version-exit")).toBeNull();
    });
  });

  it("keeps historical secret context actions observational", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const key = openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      fileId: "file-main",
      path: "src/main.ts",
    });
    applyFilesBufferLoad(PROJECT, key, {
      ...PROJECT_SOURCE_IDENTITY,
      file_id: "file-main",
      version_id: "version-current",
      path: "src/main.ts",
      content: "current\n",
      over_limit: false,
      writable: true,
      binary: false,
      size_bytes: 8,
      sha256: "current-sha",
      encoding: "utf-8",
    });
    const secret = ["AKIA", "IOS", "F7Q", "VFHT", "EXAM", "PLE1"].join("");
    const content = `key=${secret}\n`;
    const secretScreen: SecretScreen = {
      truncated: false,
      screened_bytes: content.length,
      spans: [{ start: 4, end: 4 + secret.length, state: "detected", rule_id: "aws", rule_title: "AWS access key" }],
    };
    const client = stubFilesClient({ ...getLycaonClient() });
    const readRows = client.getSourceViewRows;
    vi.spyOn(client, "getSourceViewRows").mockImplementation(async (...args) => {
      const page = await readRows(...args);
      if (page.kind !== "comparison") return page;
      return { ...page, rows: page.rows.map(row => row.text === content ? { ...row, secret_screen: secretScreen } : row) };
    });
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={client}
      />
    ));
    await screen.findByTestId("files-editor");

    setSelectedFileVersion(PROJECT, key, {
      versionId: "version-secret", fileId: "file-main", rootId: "r1", path: "src/main.ts",
      op: "write", ts: "2026-08-26T10:00:00Z", beforeAvailability: "absent",
      sha256: "old-sha", sizeBytes: content.length, availability: "available", secretScreen,
      source: { kind: "text", before: "", after: content },
    });

    const mark = await waitFor(() => {
      const found = document.querySelector<HTMLElement>(
        ".cm-den-secret--detected",
      );
      if (!found) throw new Error("historical secret mark did not render");
      expect(found.textContent).toBe(secret);
      return found;
    });
    const editor = mark.closest<HTMLElement>(".cm-editor");
    if (!editor) throw new Error("historical editor did not render");
    const view = EditorView.findFromDOM(editor);
    if (!view) throw new Error("historical editor view was unavailable");
    view.dispatch({ selection: { anchor: view.posAtDOM(mark) + 1 } });
    fireEvent.contextMenu(mark, { clientX: 10, clientY: 10 });

    expect(await screen.findByTestId("files-ctx-editor-secret-fact")).toBeTruthy();
    expect(screen.queryByTestId("files-ctx-editor-track-secret")).toBeNull();
    expect(screen.queryByTestId("files-ctx-editor-ignore-secret")).toBeNull();
    expect(screen.queryByTestId("files-ctx-editor-mark-secret")).toBeNull();
    expect(screen.queryByTestId("files-ctx-editor-inline-edit")).toBeNull();
  });

  it("renders retained versions before Git history finishes", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const key = openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      fileId: "file-main",
      path: "src/main.ts",
    });
    applyFilesBufferLoad(PROJECT, key, {
      ...PROJECT_SOURCE_IDENTITY,
      file_id: "file-main",
      version_id: "version-current",
      path: "src/main.ts",
      content: "current\n",
      over_limit: false,
      writable: true,
      binary: false,
      size_bytes: 8,
      sha256: "current-sha",
      encoding: "utf-8",
    });
    const versions = deferred<SourceFileVersionsResponse>();
    const git = deferred<SourceFileVersionsResponse>();
    const listProjectSourceVersions = vi.fn(
      (_projectId, _target, opts) => opts?.lane === "git" ? git.promise : versions.promise,
    );
    const client = {
      ...getLycaonClient()!,
      listProjectSourceVersions,
    } as LycaonClient;
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={client}
      />
    ));

    fireEvent.click(await screen.findByTestId("file-version-trigger"));
    expect(listProjectSourceVersions).toHaveBeenCalledWith(
      PROJECT,
      { fileId: "file-main" },
      expect.objectContaining({ lane: "retained" }),
    );
    versions.resolve({
      file_id: "file-main",
      current: { state: "content", sha256: "current-sha" },
      versions: [{
        id: "version-old",
        file_id: "file-main",
        workspace_kind: "project",
        root_id: "r1",
        path: "src/main.ts",
        state: "content",
        content_sha256: "old-sha",
        size_bytes: 4,
        landing: "working_file",
        capture_state: "stored",
        capture_quality: "exact",
        op: "write",
        ordinal: 1,
        turn: 1,
        created_at: "2026-08-26T10:00:00Z",
      }],
      commits: [],
      arrivals: [],
      git_history_state: "not_requested",
      next_cursor: undefined,
    });
    await waitFor(() => {
      expect(screen.getByText("Edited")).toBeTruthy();
      expect(screen.getByRole("status", { name: "Loading Git history" }))
        .toBeTruthy();
    });
    expect(listProjectSourceVersions).toHaveBeenLastCalledWith(
      PROJECT,
      { fileId: "file-main" },
      expect.objectContaining({ lane: "git" }),
    );

    git.resolve({
      file_id: "file-main",
      current: { state: "content", sha256: "current-sha" },
      versions: [],
      commits: [{
        commit: "1234567890abcdef",
        subject: "Older Git state",
        authored_at: "2026-08-25T10:00:00Z",
        committed_at: "2026-08-25T10:00:00Z",
        source_path: "src/main.ts",
        blob_oid: "abcdef",
      }],
      arrivals: [],
      git_history_state: "available",
      next_cursor: undefined,
    });
    await waitFor(() => {
      expect(screen.getByText("Older Git state")).toBeTruthy();
      expect(screen.queryByRole("status", { name: "Loading Git history" }))
        .toBeNull();
    });
  });

  it("reports a Git lane that could not answer in the project's notices, not in the picker", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const key = openFilesBuffer(PROJECT, {
      intent: "permanent",
      rootId: "r1",
      rootLabel: "repo",
      fileId: "file-main",
      path: "src/main.ts",
    });
    applyFilesBufferLoad(PROJECT, key, {
      ...PROJECT_SOURCE_IDENTITY,
      file_id: "file-main",
      version_id: "version-current",
      path: "src/main.ts",
      content: "current\n",
      over_limit: false,
      writable: true,
      binary: false,
      size_bytes: 8,
      sha256: "current-sha",
      encoding: "utf-8",
    });
    const versions = deferred<SourceFileVersionsResponse>();
    const gitPage = deferred<SourceFileVersionsResponse>();
    const notices = createNoticeStore();
    registerNoticePublisher(notices);
    onTestFinished(() => registerNoticePublisher(null));
    const gitLaneNotices = () =>
      selectProjectNoticeGroups(notices.index()).find(group => group.projectId === PROJECT)?.notices
        .filter(notice => notice.code === "files_git_history_unavailable") ?? [];
    const listProjectSourceVersions = vi.fn(
      (_projectId, _target, opts) =>
        opts?.lane === "git"
          ? gitPage.promise
          : versions.promise,
    );
    const client = {
      ...getLycaonClient()!,
      listProjectSourceVersions,
    } as LycaonClient;
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={client}
      />
    ));

    fireEvent.click(await screen.findByTestId("file-version-trigger"));
    versions.resolve({
      file_id: "file-main",
      current: { state: "content", sha256: "current-sha" },
      versions: [{
        id: "version-old",
        file_id: "file-main",
        workspace_kind: "project",
        root_id: "r1",
        path: "src/main.ts",
        state: "content",
        content_sha256: "old-sha",
        size_bytes: 4,
        landing: "working_file",
        capture_state: "stored",
        capture_quality: "exact",
        op: "write",
        ordinal: 1,
        turn: 1,
        created_at: "2026-08-26T10:00:00Z",
      }],
      commits: [],
      arrivals: [],
      git_history_state: "not_requested",
      next_cursor: undefined,
    });
    await waitFor(() => expect(screen.getByText("Edited")).toBeTruthy());

    gitPage.resolve({
      file_id: "file-main",
      current: { state: "content", sha256: "current-sha" },
      versions: [],
      commits: [],
      arrivals: [],
      git_history_state: "timed_out",
      next_cursor: undefined,
    });
    await waitFor(() => expect(gitLaneNotices()).toHaveLength(1));
    expect(gitLaneNotices()[0]?.message).toContain("took too long");
    expect(screen.getByText("Edited")).toBeTruthy();
  });

  it("starts Walk at the selected file and retains its presentation during live growth", async () => {
    const width = vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(600);
    onTestFinished(() => width.mockRestore());
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    appStore.actions.setCurrentSession({
      id: "session-walk",
      project_id: PROJECT,
      current_turn: 1,
    } as never);
    const base = getLycaonClient()!;
    const effect = (
      id: string,
      path: string,
      ordinal: number,
    ): SourceWalkEffect => sourceEffectFixture({
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
    const client = stubFilesClient({
      ...base,
      browseProjectSource: vi.fn(async () => ({
        workspace_id: "workspace-1",
        root_id: "r1",
        dir: ".",
        watch_complete: true,
        entries: [
          { name: "a.ts", is_dir: false },
          { name: "b.ts", is_dir: false },
        ],
      })),
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
      readComparison: vi.fn(async () => sourceReaderFixture().comparison({
        in_range: true,
        truncated: false,
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
    });

    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={client}
      />
    ));

    const rows = await screen.findAllByTestId("files-tree-file");
    const selected = rows.find((row) => row.textContent?.includes("b.ts"));
    expect(selected).toBeTruthy();
    fireEvent.click(selected!);
    fireEvent.click(screen.getByTestId("walk-button"));

    await waitFor(() => expect(walkState(PROJECT).status).toBe("ready"));
    expect(walkState(PROJECT).walk.steps).toHaveLength(2);
    const step = currentWalkStep(PROJECT);
    expect(step?.kind === "effect" && step.effect.path).toBe("b.ts");
    const key = fileBufferKey("r1", "b.ts", undefined, "file-b.ts");
    await waitFor(() => expect(selectedFileVersion(PROJECT, key)).not.toBeNull());
    await waitFor(() => expect(screen.getByTestId("files-pane-presentation").dataset.pending).toBe("false"));
    const presentation = selectedFileVersion(PROJECT, key);
    const publishedEditor = () => document.querySelector('.den-files-tab-deck > [data-resident="active"] [data-testid="files-editor-host"]');
    const editor = publishedEditor();
    expect(editor).not.toBeNull();
    const activeBufferKey = projectFilesState(PROJECT).activeKey;

    effects.push(effect("effect-c", "c.ts", 3));
    await refreshWalk(PROJECT, client);

    expect(walkState(PROJECT).walk.steps).toHaveLength(3);
    expect(currentWalkStep(PROJECT)?.key).toBe(step?.key);
    expect(selectedFileVersion(PROJECT, key)).toBe(presentation);
    expect(publishedEditor()).toBe(editor);
    expect(projectFilesState(PROJECT).activeKey).toBe(activeBufferKey);

    const tab = [...document.querySelectorAll<HTMLElement>(".den-files-tab")].find((entry) => entry.dataset.key === key)!;
    const scroller = document.querySelector<HTMLElement>(".den-files-tabs-scroller")!;
    const box = (left: number) => ({ left, right: left + 100, top: 0, bottom: 24, width: 100, height: 24, x: left, y: 0, toJSON: () => ({}) }) as DOMRect;
    // The selected tab sits past the strip's right edge, so a reveal glides by 200.
    scroller.getBoundingClientRect = () => box(0);
    tab.getBoundingClientRect = () => box(200);
    scroller.scrollLeft = 0;
    fireEvent.click(document.querySelector('[data-testid="step-bar-dot"][aria-current="step"]')!);
    await waitFor(() => expect(scroller.scrollLeft).toBe(200));
    await refreshWalk(PROJECT, client);
    for (let frame = 0; frame < 3; frame++) await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    expect(scroller.scrollLeft).toBe(200);
  });
});
