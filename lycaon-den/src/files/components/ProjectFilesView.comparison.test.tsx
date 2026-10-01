import "../../test/document-outbox-fixture.ts";
import { stubFilesClient } from "../../test/source-client-fixture.ts";
import type { SourceComparison } from "../../api/types.ts";
import {
  PROJECT,
  loadedBuffer,
  editorProps,
  seedScope,
  resetProjectFilesViewTest,
} from "./project-files-view-test-harness.ts";

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { foldedRanges } from "@codemirror/language";
import { EditorView } from "@codemirror/view";
import { createSignal } from "solid-js";
import type { FileVersionView } from "../history/file-version.ts";
import { FilesEditor } from "../editor/FilesEditor.tsx";
import { openFilesBuffer } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { setSidebarScope } from "../review/review-pane.ts";
import { editorScopeDiffOriginal } from "../../components/source/diff/scope-diff.ts";
import { resetEditorPrefsForTests } from "../../settings/editor/editor-prefs.ts";
import { createPreparation } from "../../ui/presentation.ts";
import { PresentationProvider } from "../../ui/presentation-context.tsx";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";

describe("editor shows the eye's comparison", () => {
  const SHA = "tip-sha";
  const LONG_BEFORE = `${Array.from({ length: 80 }, (_, index) => `line ${index}`).join("\n")}\n`;
  const LONG_AFTER = LONG_BEFORE.replace("line 40\n", "line 40 changed\n");

  const viewIn = (container: HTMLElement): EditorView => {
    const editor = container.querySelector<HTMLElement>(".cm-editor");
    const view = editor ? EditorView.findFromDOM(editor) : null;
    if (!view) throw new Error("the editor view is not attached yet");
    return view;
  };
  const foldedRangeCount = (container: HTMLElement) =>
    foldedRanges(viewIn(container).state).size;

  const scopedClient = (before = "one\n") =>
    stubFilesClient(({
      getProjectSourceComparison: vi.fn(async (): Promise<SourceComparison> => ({
        truncated: false,
        in_range: true,
        before: { state: before ? "content" : "absent", size_bytes: before.length, availability: before ? "available" : "absent", content: before },
        after: { state: "content", size_bytes: 8, availability: "available", content: "one\ntwo\n", sha256: SHA },
        location_changed: false,
      })),
      listProjectSourceSymbols: vi.fn(async () => ({ symbols: [] })),
      getProjectSourceAttribution: vi.fn(async () => ({
        head_sha256: SHA,
        intervals: [],
      })),
      listCodeScans: vi.fn(async () => ({ scans: [] })),
    }));

  const markScope = async () => {
    setSidebarScope(PROJECT, { kind: "commit" });
    await seedScope(PROJECT, [
      { rootId: "r1", path: "src/main.ts", changeId: "c1", sha: SHA },
    ]);
  };

  const markCreateScope = async () => {
    setSidebarScope(PROJECT, { kind: "new" });
    await seedScope(PROJECT, [
      {
        rootId: "r1",
        path: "src/main.ts",
        changeId: "c1",
        sha: SHA,
        op: "create",
      },
    ]);
  };

  beforeEach(resetProjectFilesViewTest);

  it("leaves a created file untinted after resolving its absent baseline", async () => {
    const buf = loadedBuffer({ content: "hello\nworld\n", sha256: SHA });
    await markCreateScope();
    const client = scopedClient("");
    const { container } = render(() => (
      <FilesEditor {...editorProps(buf)} client={client} />
    ));

    await waitFor(() => {
      expect(container.querySelector(".cm-content")).toBeTruthy();
    });
    expect(
      container.querySelector(".cm-deletedChunk, .cm-changedLine"),
    ).toBeNull();
    await waitFor(() => expect(client.getProjectSourceComparison).toHaveBeenCalledTimes(1));
    expect(container.querySelector(".cm-deletedChunk, .cm-changedLine")).toBeNull();
  });

  it("keeps a created file untinted when it enters the eye after opening", async () => {
    const buf = loadedBuffer({ content: "hello\nworld\n", sha256: SHA });
    setSidebarScope(PROJECT, { kind: "new" });
    const client = scopedClient("");
    const { container } = render(() => (
      <FilesEditor {...editorProps(buf)} client={client} />
    ));

    await waitFor(() => {
      expect(container.querySelector(".cm-content")).toBeTruthy();
    });
    expect(container.querySelector(".cm-changedLine")).toBeNull();

    await seedScope(PROJECT, [
      {
        rootId: "r1",
        path: "src/main.ts",
        changeId: "c1",
        sha: SHA,
        op: "create",
      },
    ]);

    await Promise.resolve();
    expect(
      container.querySelector(".cm-deletedChunk, .cm-changedLine"),
    ).toBeNull();
    await waitFor(() => expect(client.getProjectSourceComparison).toHaveBeenCalledTimes(1));
    expect(container.querySelector(".cm-deletedChunk, .cm-changedLine")).toBeNull();
  });

  it("leaves a created file untinted when its buffer sha differs", async () => {
    const buf = loadedBuffer({ content: "hello\nworld\n", sha256: "other-sha" });
    await markCreateScope();
    const client = scopedClient("");
    const { container } = render(() => (
      <FilesEditor {...editorProps(buf)} client={client} />
    ));

    await waitFor(() => {
      expect(container.querySelector(".cm-content")).toBeTruthy();
    });
    expect(
      container.querySelector(".cm-deletedChunk, .cm-changedLine"),
    ).toBeNull();
    expect(client.getProjectSourceComparison).not.toHaveBeenCalled();
  });

  it("paints the scope comparison into the buffer", async () => {
    const buf = loadedBuffer({ content: "one\ntwo\n", sha256: SHA });
    await markScope();
    const client = scopedClient();
    const { container } = render(() => (
      <FilesEditor {...editorProps(buf)} client={client} />
    ));

    await waitFor(() => {
      expect(container.querySelector(".cm-deletedChunk, .cm-changedLine")).toBeTruthy();
    });
    expect(client.getProjectSourceComparison).toHaveBeenCalledWith(
      PROJECT,
      { path: "src/main.ts", rootId: "r1", baseline: "commit", expectedHead: undefined },
      expect.anything(),
    );
  });

  it("does not refetch the open diff when an unrelated file changes", async () => {
    const buf = loadedBuffer({ content: "one\ntwo\n", sha256: SHA });
    await markScope();
    const client = scopedClient();
    render(() => <FilesEditor {...editorProps(buf)} client={client} />);

    await waitFor(() => {
      expect(client.getProjectSourceComparison).toHaveBeenCalledTimes(1);
    });

    await seedScope(PROJECT, [
      { rootId: "r1", path: "src/main.ts", changeId: "c1", sha: SHA },
      {
        rootId: "r1",
        path: ".task/last-run/result.json",
        changeId: "noise-2",
        sha: "noise-sha-2",
      },
    ]);
    await Promise.resolve();

    expect(client.getProjectSourceComparison).toHaveBeenCalledTimes(1);
  });

  it("puts the change bar against the code, and draws only one", async () => {
    const buf = loadedBuffer({ content: "one\ntwo\n", sha256: SHA });
    await markScope();
    const { container } = render(() => (
      <FilesEditor {...editorProps(buf)} client={scopedClient()} />
    ));

    await waitFor(() => {
      expect(
        container.querySelector(".files-line-gutter__cell--add"),
      ).toBeTruthy();
    });

    const columns = [...container.querySelectorAll(".cm-gutter")].map(
      (g) => g.className,
    );
    expect(columns).toHaveLength(1);
    expect(columns[0]).toContain("files-line-gutter");

    const line = container.querySelector<HTMLElement>(".cm-changedLine");
    expect(line).toBeTruthy();
    expect(line!.style.boxShadow).toBe("");
  });

  it("clears the comparison when the file leaves the active scope", async () => {
    const buf = loadedBuffer({ content: "one\ntwo\n", sha256: SHA });
    await markScope();
    const { container } = render(() => (
      <FilesEditor {...editorProps(buf)} client={scopedClient()} />
    ));

    await waitFor(() => {
      expect(container.querySelector(".cm-deletedChunk, .cm-changedLine")).toBeTruthy();
    });

    await seedScope(PROJECT, []);

    await waitFor(() => {
      expect(container.querySelector(".cm-deletedChunk, .cm-changedLine")).toBeNull();
    });
  });

  it("drops the comparison when the eye moves to a scope without this file", async () => {
    const buf = loadedBuffer({ content: "one\ntwo\n", sha256: SHA });
    await markScope();
    const { container } = render(() => (
      <FilesEditor {...editorProps(buf)} client={scopedClient()} />
    ));

    await waitFor(() => {
      expect(container.querySelector(".cm-deletedChunk, .cm-changedLine")).toBeTruthy();
    });

    setSidebarScope(PROJECT, { kind: "new" });
    await seedScope(PROJECT, []);

    await waitFor(() => {
      expect(container.querySelector(".cm-deletedChunk, .cm-changedLine")).toBeNull();
    });
  });

  it("clears the comparison when the buffer no longer matches the change", async () => {
    const buf = loadedBuffer({ content: "one\ntwo\n", sha256: "stale-sha" });
    await markScope();
    const client = scopedClient();
    const { container } = render(() => (
      <FilesEditor {...editorProps(buf)} client={client} />
    ));

    await waitFor(() => {
      expect(container.querySelector(".cm-content")).toBeTruthy();
    });
    expect(client.getProjectSourceComparison).not.toHaveBeenCalled();
    expect(container.querySelector(".cm-deletedChunk, .cm-changedLine")).toBeNull();
  });

  it("shows nothing when the scope holds no change for this file", async () => {
    const buf = loadedBuffer({ content: "one\ntwo\n", sha256: SHA });
    setSidebarScope(PROJECT, { kind: "commit" });
    await seedScope(PROJECT, [
      { rootId: "r1", path: "src/other.ts", changeId: "c2", sha: SHA },
    ]);
    const client = scopedClient();
    const { container } = render(() => (
      <FilesEditor {...editorProps(buf)} client={client} />
    ));

    await waitFor(() => {
      expect(container.querySelector(".cm-content")).toBeTruthy();
    });
    expect(client.getProjectSourceComparison).not.toHaveBeenCalled();
    expect(container.querySelector(".cm-deletedChunk, .cm-changedLine")).toBeNull();
  });

  describe("opened from review", () => {
    it("prepares the folded comparison before publishing each review file", async () => {
      for (const path of ["src/main.ts", "src/next.ts"]) {
        loadedBuffer({ path, content: LONG_AFTER, sha256: SHA });
        const key = openFilesBuffer(PROJECT, {
          rootId: "r1", rootLabel: "repo", path, intent: "transient", condenseContext: true,
        });
        const buf = projectFilesState(PROJECT).byKey[key]!;
        setSidebarScope(PROJECT, { kind: "commit" });
        await seedScope(PROJECT, [{ rootId: "r1", path, changeId: "c1", sha: SHA }]);
        const client = longClient();
        const comparison = await client.getProjectSourceComparison(PROJECT, { rootId: "r1", path, baseline: "commit" });
        let resolve!: (value: typeof comparison) => void;
        const pending = new Promise<typeof comparison>((accept) => { resolve = accept; });
        vi.mocked(client.getProjectSourceComparison).mockClear().mockReturnValue(pending);
        const preparation = createPreparation();
        const [active, setActive] = createSignal(false);
        const { container, unmount } = render(() => (
          <PresentationProvider preparation={preparation}>
            <ResidentPresenceProvider presence={active() ? "active" : "pending"}>
              <FilesEditor {...editorProps(buf)} client={client} />
            </ResidentPresenceProvider>
          </PresentationProvider>
        ));
        await new Promise<void>((done) => requestAnimationFrame(() => requestAnimationFrame(() => done())));
        expect(preparation.ready()).toBe(false);
        expect(client.getProjectSourceComparison).toHaveBeenCalledTimes(1);
        resolve(comparison);
        await waitFor(() => expect(preparation.ready()).toBe(true));
        expect(foldedRangeCount(container)).toBe(2);
        setActive(true);
        expect(foldedRangeCount(container)).toBe(2);
        expect(client.getProjectSourceComparison).toHaveBeenCalledTimes(1);
        unmount();
      }
    });

    const longClient = () =>
      stubFilesClient(({
        getProjectSourceComparison: vi.fn(async (): Promise<SourceComparison> => ({
          truncated: false,
          in_range: true,
          before: { state: "content", size_bytes: LONG_BEFORE.length, availability: "available", content: LONG_BEFORE },
          after: { state: "content", size_bytes: LONG_AFTER.length, availability: "available", content: LONG_AFTER, sha256: SHA },
          location_changed: false,
        })),
        listProjectSourceSymbols: vi.fn(async () => ({ symbols: [] })),
        getProjectSourceAttribution: vi.fn(async () => ({ head_sha256: SHA, intervals: [] })),
        listCodeScans: vi.fn(async () => ({ scans: [] })),
      }));

    it.each(["failed", "missing"])("settles preparation when the comparison is %s", async (outcome) => {
      loadedBuffer({ content: LONG_AFTER, sha256: SHA });
      const key = openFilesBuffer(PROJECT, {
        rootId: "r1", rootLabel: "repo", path: "src/main.ts", intent: "transient", condenseContext: true,
      });
      await markScope();
      const client = longClient();
      if (outcome === "failed") vi.mocked(client.getProjectSourceComparison).mockRejectedValue(new Error("offline"));
      else vi.mocked(client.getProjectSourceComparison).mockResolvedValue({ in_range: false, location_changed: false, truncated: false });
      const preparation = createPreparation();
      const { container } = render(() => (
        <PresentationProvider preparation={preparation}>
          <ResidentPresenceProvider presence="pending">
            <FilesEditor {...editorProps(projectFilesState(PROJECT).byKey[key]!)} client={client} />
          </ResidentPresenceProvider>
        </PresentationProvider>
      ));
      await waitFor(() => expect(client.getProjectSourceComparison).toHaveBeenCalled());
      await waitFor(() => expect(preparation.ready()).toBe(true));
      expect(projectFilesState(PROJECT).byKey[key]?.condenseContext).toBe(false);
      expect(foldedRangeCount(container)).toBe(0);
    });

    it("folds unchanged runs like a walk step, and Full file unfolds them", async () => {
      loadedBuffer({ content: LONG_AFTER, sha256: SHA });
      const key = openFilesBuffer(PROJECT, {
        rootId: "r1",
        rootLabel: "repo",
        path: "src/main.ts",
        intent: "transient",
        condenseContext: true,
      });
      const buf = projectFilesState(PROJECT).byKey[key]!;
      await markScope();
      const { container } = render(() => (
        <FilesEditor {...editorProps(buf)} client={longClient()} />
      ));

      await waitFor(() => {
        expect(foldedRangeCount(container)).toBe(2);
      });
      expect(container.querySelector(".cm-foldPlaceholder")).toBeTruthy();
      expect(projectFilesState(PROJECT).byKey[key]?.condenseContext).toBe(false);

      fireEvent.click(await screen.findByRole("button", { name: "Full file" }));
      await waitFor(() => {
        expect(container.querySelector(".cm-foldPlaceholder")).toBeNull();
      });
      expect(screen.queryByRole("button", { name: "Full file" })).toBeNull();
    });

    it("folds again when review reopens a file already unfolded", async () => {
      loadedBuffer({ content: LONG_AFTER, sha256: SHA });
      const open = () =>
        openFilesBuffer(PROJECT, {
          rootId: "r1",
          rootLabel: "repo",
          path: "src/main.ts",
          intent: "transient",
          condenseContext: true,
        });
      const buf = projectFilesState(PROJECT).byKey[open()]!;
      await markScope();
      const { container } = render(() => (
        <FilesEditor {...editorProps(buf)} client={longClient()} />
      ));

      await waitFor(() => {
        expect(foldedRangeCount(container)).toBe(2);
      });
      fireEvent.click(await screen.findByRole("button", { name: "Full file" }));
      await waitFor(() => {
        expect(foldedRangeCount(container)).toBe(0);
      });

      open();
      await waitFor(() => {
        expect(foldedRangeCount(container)).toBe(2);
      });
      expect(screen.getByRole("button", { name: "Full file" })).toBeTruthy();
    });

    it("leaves a file opened elsewhere unfolded", async () => {
      const buf = loadedBuffer({ content: LONG_AFTER, sha256: SHA });
      await markScope();
      const { container } = render(() => (
        <FilesEditor {...editorProps(buf)} client={longClient()} />
      ));

      await waitFor(() => {
        expect(editorScopeDiffOriginal(viewIn(container).state)).toBe(LONG_BEFORE);
      });
      expect(foldedRangeCount(container)).toBe(0);
      expect(screen.queryByRole("button", { name: "Full file" })).toBeNull();
    });
  });

  it("shows a historical file with condensed context and no toolbar mode", async () => {
    const buf = loadedBuffer({ content: LONG_BEFORE, sha256: SHA });
    const { container } = render(() => (
      <FilesEditor
        {...editorProps(buf)}
        client={scopedClient()}
        version={{ versionId: "version-40",
fileId: buf.fileId ?? "file-main",
rootId: buf.rootId,
path: buf.path,
op: "write",
ts: "2026-08-24T12:00:00Z",
beforeAvailability: "available",
sha256: SHA,
sizeBytes: LONG_AFTER.length,
availability: "available", source: { kind: "text", before: LONG_BEFORE, after: LONG_AFTER } }}
      />
    ));

    await waitFor(() => {
      expect(container.querySelectorAll(".cm-den-reader-gap").length).toBeGreaterThan(0);
    });
    expect(container.querySelector(".den-file-version__host .cm-editor")).toBeTruthy();
    expect(screen.queryByTestId("file-version-mode")).toBeNull();
    expect(container.querySelector(".den-file-version__stat")).toBeNull();

    fireEvent.click(await screen.findByRole("button", { name: "Full file" }));
    await waitFor(() => {
      expect(container.querySelector(".cm-den-reader-gap")).toBeNull();
    });
    expect(screen.queryByRole("button", { name: "Full file" })).toBeNull();
  });

  it("offers the exit ahead of the version actions", async () => {
    const buf = loadedBuffer({ content: "one\n", sha256: SHA });
    const props = editorProps(buf);
    render(() => (
      <FilesEditor
        {...props}
        client={scopedClient()}
        version={{ versionId: "version-one",
fileId: buf.fileId ?? "file-main",
rootId: buf.rootId,
path: buf.path,
op: "write",
ts: "2026-08-24T12:00:00Z",
beforeAvailability: "available",
sha256: SHA,
sizeBytes: 8,
availability: "available", source: { kind: "text", before: "one\n", after: "one\ntwo\n" } }}
      />
    ));

    const exit = await screen.findByTestId("file-version-exit");
    const actions = exit.parentElement;
    expect(actions?.firstElementChild).toBe(exit);
    expect(actions?.contains(screen.getByTestId("file-version-compare-trigger"))).toBe(true);
    expect(exit.getAttribute("aria-label")).toBe("Back to the current file");
    fireEvent.click(exit);
    expect(props.onSelectCurrent).toHaveBeenCalledTimes(1);
  });

  it("keeps the exit when the version has no readable bytes", async () => {
    const buf = loadedBuffer({ content: "one\n", sha256: SHA });
    const props = editorProps(buf);
    render(() => (
      <FilesEditor
        {...props}
        client={scopedClient()}
        version={{ versionId: "version-binary",
fileId: buf.fileId ?? "file-main",
rootId: buf.rootId,
path: buf.path,
op: "write",
ts: "2026-08-24T12:00:00Z",
beforeAvailability: "unavailable",
sha256: null,
sizeBytes: 0,
availability: "not_captured", source: { kind: "text", before: "", after: "" } }}
      />
    ));

    const exit = await screen.findByTestId("file-version-exit");
    expect(screen.queryByTestId("file-version-compare-trigger")).toBeNull();
    expect(screen.queryByRole("button", { name: "Full file" })).toBeNull();
    fireEvent.click(exit);
    expect(props.onSelectCurrent).toHaveBeenCalledTimes(1);
  });

  it("presents the newly selected version through the paged reader", async () => {
    const buf = loadedBuffer({ content: "current\n", sha256: SHA });
    const initial: FileVersionView = { versionId: "version-one",
fileId: buf.fileId ?? "file-main",
rootId: buf.rootId,
path: buf.path,
op: "write",
ts: "2026-08-24T12:00:00Z",
beforeAvailability: "available",
sha256: "one-sha",
sizeBytes: 4,
availability: "available", source: { kind: "text", before: "zero\n", after: "one\n" } };
    const [version, setVersion] = createSignal(initial);
    render(() => (
      <FilesEditor
        {...editorProps(buf)}
        client={scopedClient()}
        version={version()}
      />
    ));
    const host = await screen.findByTestId("file-version-file");
    await waitFor(() => expect(EditorView.findFromDOM(host)).toBeTruthy());

    setVersion({
      ...initial,
      versionId: "version-two",
      source: { kind: "text", before: "zero\n", after: "two\n" },
      sha256: "two-sha",
    });

    await waitFor(() => expect(screen.getByTestId("file-version-file").querySelector(".cm-den-reader-add")?.textContent).toBe("two"));
    expect(screen.getAllByTestId("source-reader")).toHaveLength(1);
  });

  it("defaults history previews to Current and can show what the version changed", async () => {
    const buf = {
      ...loadedBuffer({ content: "current\n", sha256: SHA }),
      baseContent: "stale base\n",
      draft: "current\n",
    };
    const version: FileVersionView = { versionId: "version-one",
fileId: buf.fileId ?? "file-main",
rootId: buf.rootId,
path: buf.path,
op: "write",
ts: "2026-08-24T12:00:00Z",
beforeAvailability: "available",
sha256: "selected-sha",
sizeBytes: 9,
availability: "available", source: { kind: "text", before: "before\n", after: "selected\n" } };
    render(() => (
      <FilesEditor {...editorProps(buf)} client={scopedClient()} version={version} />
    ));

    const host = await screen.findByTestId("file-version-file");
    await waitFor(() => expect(host.querySelector(".cm-den-reader-delete")?.textContent).toBe("current"));
    expect(screen.getByTestId("file-version-compare-trigger").textContent)
      .toContain("Compare: Current");

    fireEvent.click(screen.getByTestId("file-version-compare-trigger"));
    await waitFor(() => {
      expect(screen.getByTestId("context-menu")).toBeTruthy();
    });
    expect(screen.getByTestId("file-version-compare-before")).toBeTruthy();
    fireEvent.click(screen.getByTestId("file-version-compare-before"));

    await waitFor(() => expect(screen.getByTestId("file-version-file").querySelector(".cm-den-reader-delete")?.textContent).toBe("before"));
    expect(screen.getByTestId("file-version-compare-trigger").textContent)
      .toContain("Compare: Before");
  });

  it("uses the Editor preference for new history previews", async () => {
    resetEditorPrefsForTests({ versionComparison: "before" });
    const buf = loadedBuffer({ content: "current\n", sha256: SHA });
    const version: FileVersionView = { versionId: "preferred-version",
fileId: buf.fileId ?? "file-main",
rootId: buf.rootId,
path: buf.path,
op: "write",
ts: "2026-08-24T12:00:00Z",
beforeAvailability: "available",
sha256: "selected-sha",
sizeBytes: 9,
availability: "available", source: { kind: "text", before: "before\n", after: "selected\n" } };
    render(() => (
      <FilesEditor {...editorProps(buf)} client={scopedClient()} version={version} />
    ));

    const host = await screen.findByTestId("file-version-file");
    await waitFor(() => expect(host.querySelector(".cm-den-reader-delete")?.textContent).toBe("before"));
    expect(screen.getByTestId("file-version-compare-trigger").textContent)
      .toContain("Compare: Before");
  });

  it("starts Walk versions on the producing change", async () => {
    const buf = loadedBuffer({ content: "current\n", sha256: SHA });
    const version: FileVersionView = { versionId: "walk-version",
fileId: buf.fileId ?? "file-main",
rootId: buf.rootId,
path: buf.path,
op: "write",
ts: "2026-08-24T12:00:00Z",
beforeAvailability: "available",
sha256: "selected-sha",
sizeBytes: 9,
availability: "available",
initialComparison: "before", source: { kind: "text", before: "before\n", after: "selected\n" } };
    render(() => (
      <FilesEditor {...editorProps(buf)} client={scopedClient()} version={version} />
    ));

    const host = await screen.findByTestId("file-version-file");
    await waitFor(() => expect(host.querySelector(".cm-den-reader-delete")?.textContent).toBe("before"));
    expect(screen.getByTestId("file-version-compare-trigger").textContent)
      .toContain("Compare: Before");
  });
});
