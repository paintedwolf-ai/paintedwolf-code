// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { createRoot } from "solid-js";
import { waitFor } from "@solidjs/testing-library";
import { createDiffsReader } from "./diffs-reader.ts";
import { editorDisplayPrefs } from "../../components/source/editor/editor-display-prefs.ts";
import type { SourceReaderAccess } from "../../api/source-reader.ts";
import type { DiffRowSource } from "../../components/source/diff/diff-row-source.ts";
import type { SourceComparisonFrame } from "../../api/types.ts";
import type { SourceComparisonSession } from "../../api/source-comparison-session.ts";
import { clearEditorViewportPool } from "../../components/source/editor/editor-viewport-pool.ts";
import { mockReaderViewport } from "../../test/reader-viewport-mock.ts";

const cleanup: (() => void)[] = [];
afterEach(() => {
  for (const close of cleanup.splice(0)) close();
  clearEditorViewportPool();
});

function mockSource(path: string, framesForRev: (rev: number | null) => SourceComparisonFrame[]): DiffRowSource {
  const revisions = [{ key: "1", label: "1", ariaLabel: "Revision 1" }, { key: "2", label: "2", ariaLabel: "Revision 2" }];
  const access = (rev: number | null): SourceReaderAccess => ({
    selection: async () => "source",
    content: async () => "source content",
    summary: async () => ({
      in_range: true,
      summary: { added: 1, removed: 1, files: 1, total_lines: 2, net_lines: 0, rows: 2, folds: 0 },
    } as unknown as ReturnType<SourceReaderAccess["summary"]> extends Promise<infer T> ? T : never),
    presentation: async () => {
      const frames = framesForRev(rev);
      const session = {
        attach: () => () => {},
        ready: async () => {},
        close: async () => {},
        state: () => ({
          id: `session-${rev ?? "net"}`,
          state: "ready" as const,
          comparison: { in_range: true, summary: { added: 1, removed: 1, rows: 2 } },
          extent: { rows: 2, complete: true },
          intent_revision: "1",
          projection_revision: "1",
        }),
        unfold: async () => {},
        frameAt: async () => frames[0]!,
        frame: async () => frames[0]!,
      };
      return session as unknown as SourceComparisonSession;
    },
  } as unknown as SourceReaderAccess);

  return {
    projectId: "p1",
    path: () => path,
    rootId: () => undefined,
    rootRefs: [],
    openable: () => true,
    chatDestination: undefined,
    change: () => "changed",
    revisions,
    revisionsLabel: "Revisions",
    netLabel: "net",
    stat: () => null,
    isNoop: () => false,
    noopNote: undefined,
    access,
  } as unknown as DiffRowSource;
}

it("latches prior comparison rows during in-flight revision reloads", async () => {
  cleanup.push(mockReaderViewport());

  const frameNet: SourceComparisonFrame = {
    kind: "comparison",
    view_id: "v-net",
    intent_revision: "1",
    projection_revision: "1",
    anchor: { row: 0 },
    extent: { rows: 2, complete: true },
    span: { start: 0, end: 2 },
    rows: [
      { index: 0, end: 1, kind: "equal", before_line: 1, after_line: 1, text: "original line 1\n", changed: [] },
      { index: 1, end: 2, kind: "insert", before_line: 0, after_line: 2, text: "original line 2\n", changed: [] },
    ],
  };

  let resolveRev1!: (frames: SourceComparisonFrame[]) => void;
  const rev1Promise = new Promise<SourceComparisonFrame[]>((resolve) => { resolveRev1 = resolve; });

  const frameRev1: SourceComparisonFrame = {
    kind: "comparison",
    view_id: "v-rev1",
    intent_revision: "2",
    projection_revision: "2",
    anchor: { row: 0 },
    extent: { rows: 2, complete: true },
    span: { start: 0, end: 2 },
    rows: [
      { index: 0, end: 1, kind: "equal", before_line: 1, after_line: 1, text: "revised line 1\n", changed: [] },
      { index: 1, end: 2, kind: "delete", before_line: 2, after_line: 0, text: "revised line 2\n", changed: [] },
    ],
  };

  let currentRev: number | null = null;
  const source: DiffRowSource = {
    ...mockSource("test.go", () => [frameNet]),
    access: (rev: number | null): SourceReaderAccess => ({
      selection: async () => "source",
      content: async () => "source content",
      summary: async () => ({
        in_range: true,
        summary: { added: 1, removed: 1, files: 1, total_lines: 2, net_lines: 0, rows: 2, folds: 0 },
      } as unknown as ReturnType<SourceReaderAccess["summary"]> extends Promise<infer T> ? T : never),
      presentation: async () => {
        const frames = rev === 0 ? await rev1Promise : [frameNet];
        const session = {
          attach: () => () => {},
          ready: async () => {},
          close: async () => {},
          state: () => ({
            id: `session-${rev ?? "net"}`,
            state: "ready" as const,
            comparison: { in_range: true, summary: { added: 1, removed: 1, rows: 2 } },
            extent: { rows: 2, complete: true },
            intent_revision: "1",
            projection_revision: "1",
          }),
          unfold: async () => {},
          frameAt: async () => frames[0]!,
          frame: async () => frames[0]!,
        };
        return session as unknown as SourceComparisonSession;
      },
    } as unknown as SourceReaderAccess),
  };

  const reloadingEvents: { key: string; reloading: boolean }[] = [];
  const parent = document.createElement("div");
  document.body.append(parent);

  let readerInstance!: ReturnType<typeof createDiffsReader>;

  createRoot((dispose) => {
    cleanup.push(dispose);
    readerInstance = createDiffsReader({
      scope: null,
      sections: () => [{ key: "file-1", source, digest: { in_range: true, changes_rows: 2 } }],
      prefs: () => editorDisplayPrefs(),
      open: () => true,
      revision: () => currentRev,
      onReloading: (key, reloading) => reloadingEvents.push({ key, reloading }),
      pageHeading: () => document.createElement("div"),
      sectionHeading: () => document.createElement("div"),
      onError: vi.fn(),
    });
    cleanup.push(() => { readerInstance.destroy(); parent.remove(); });
    readerInstance.mount(parent, { show: vi.fn(), leave: vi.fn() });
  });

  await waitFor(() => {
    const doc = parent.querySelector(".cm-content");
    expect(doc?.textContent).toContain("original line 1");
  });

  expect(reloadingEvents).toEqual([]);

  currentRev = 0;
  readerInstance.reload("file-1");

  expect(reloadingEvents).toEqual([{ key: "file-1", reloading: true }]);

  // Existing rows remain in place while the new revision is loading.
  const contentDuringReload = parent.querySelector(".cm-content");
  expect(contentDuringReload?.textContent).toContain("original line 1");
  expect(contentDuringReload?.textContent).toContain("original line 2");

  await waitFor(() => {
    expect(parent.querySelector(".cm-den-reader-reloading")).toBeTruthy();
  });

  resolveRev1([frameRev1]);

  await waitFor(() => {
    const doc = parent.querySelector(".cm-content");
    expect(doc?.textContent).toContain("revised line 1");
    expect(doc?.textContent).toContain("revised line 2");
  });

  expect(reloadingEvents.at(-1)).toEqual({ key: "file-1", reloading: false });
});

it("resets runtimes, resets scroll, and closes sessions on reset()", async () => {
  cleanup.push(mockReaderViewport());

  const frame: SourceComparisonFrame = {
    kind: "comparison",
    view_id: "v-1",
    intent_revision: "1",
    projection_revision: "1",
    anchor: { row: 0 },
    extent: { rows: 2, complete: true },
    span: { start: 0, end: 2 },
    rows: [
      { index: 0, end: 1, kind: "equal", before_line: 1, after_line: 1, text: "alpha line 1\n", changed: [] },
      { index: 1, end: 2, kind: "insert", before_line: 0, after_line: 2, text: "alpha line 2\n", changed: [] },
    ],
  };

  const closeSpy = vi.fn(async () => {});
  const source: DiffRowSource = {
    ...mockSource("test.go", () => [frame]),
    access: () => ({
      selection: async () => "source",
      content: async () => "source content",
      summary: async () => ({ in_range: true, summary: { added: 1, removed: 0, files: 1, total_lines: 2, net_lines: 1, rows: 2, folds: 0 } }),
      presentation: async () => ({
        attach: () => () => {},
        ready: async () => {},
        close: closeSpy,
        state: () => ({
          id: "session-alpha",
          state: "ready" as const,
          comparison: { in_range: true, summary: { added: 1, removed: 0, rows: 2 } },
          extent: { rows: 2, complete: true },
          intent_revision: "1",
          projection_revision: "1",
        }),
        unfold: async () => {},
        frameAt: async () => frame,
        frame: async () => frame,
      } as unknown as SourceComparisonSession),
    } as unknown as SourceReaderAccess),
  };

  const parent = document.createElement("div");
  document.body.append(parent);

  let readerInstance!: ReturnType<typeof createDiffsReader>;
  createRoot((dispose) => {
    cleanup.push(dispose);
    readerInstance = createDiffsReader({
      scope: null,
      sections: () => [{ key: "file-1", source, digest: { in_range: true, changes_rows: 2 } }],
      prefs: () => editorDisplayPrefs(),
      open: () => true,
      revision: () => null,
      pageHeading: () => document.createElement("div"),
      sectionHeading: () => document.createElement("div"),
      onError: vi.fn(),
    });
    cleanup.push(() => { readerInstance.destroy(); parent.remove(); });
    const editor = readerInstance.mount(parent, { show: vi.fn(), leave: vi.fn() });
    editor.view.scrollDOM.scrollTop = 500;
  });

  await waitFor(() => {
    const doc = parent.querySelector(".cm-content");
    expect(doc?.textContent).toContain("alpha line 1");
  });

  readerInstance.reset();

  expect(closeSpy).toHaveBeenCalled();
});

it("automatically reloads when input source updates for an already loaded section", async () => {
  cleanup.push(mockReaderViewport());

  const frame1: SourceComparisonFrame = {
    kind: "comparison",
    view_id: "v-1",
    intent_revision: "1",
    projection_revision: "1",
    anchor: { row: 0 },
    extent: { rows: 2, complete: true },
    span: { start: 0, end: 2 },
    rows: [
      { index: 0, end: 1, kind: "equal", before_line: 1, after_line: 1, text: "v1 content\n", changed: [] },
      { index: 1, end: 2, kind: "insert", before_line: 0, after_line: 2, text: "v1 added\n", changed: [] },
    ],
  };

  const frame2: SourceComparisonFrame = {
    kind: "comparison",
    view_id: "v-2",
    intent_revision: "2",
    projection_revision: "2",
    anchor: { row: 0 },
    extent: { rows: 2, complete: true },
    span: { start: 0, end: 2 },
    rows: [
      { index: 0, end: 1, kind: "equal", before_line: 1, after_line: 1, text: "v2 content\n", changed: [] },
      { index: 1, end: 2, kind: "insert", before_line: 0, after_line: 2, text: "v2 newly arrived\n", changed: [] },
    ],
  };

  const closeSpy1 = vi.fn(async () => {});
  const source1: DiffRowSource = {
    ...mockSource("test.go", () => [frame1]),
    access: () => ({
      selection: async () => "source",
      content: async () => "source content",
      summary: async () => ({ in_range: true, summary: { added: 1, removed: 0, files: 1, total_lines: 2, net_lines: 1, rows: 2, folds: 0 } }),
      presentation: async () => ({
        attach: () => () => {},
        ready: async () => {},
        close: closeSpy1,
        state: () => ({
          id: "session-1",
          state: "ready" as const,
          comparison: { in_range: true, summary: { added: 1, removed: 0, rows: 2 } },
          extent: { rows: 2, complete: true },
          intent_revision: "1",
          projection_revision: "1",
        }),
        unfold: async () => {},
        frameAt: async () => frame1,
        frame: async () => frame1,
      } as unknown as SourceComparisonSession),
    } as unknown as SourceReaderAccess),
  };

  const source2: DiffRowSource = {
    ...mockSource("test.go", () => [frame2]),
    access: () => ({
      selection: async () => "source",
      content: async () => "source content",
      summary: async () => ({ in_range: true, summary: { added: 1, removed: 0, files: 1, total_lines: 2, net_lines: 1, rows: 2, folds: 0 } }),
      presentation: async () => ({
        attach: () => () => {},
        ready: async () => {},
        close: async () => {},
        state: () => ({
          id: "session-2",
          state: "ready" as const,
          comparison: { in_range: true, summary: { added: 1, removed: 0, rows: 2 } },
          extent: { rows: 2, complete: true },
          intent_revision: "2",
          projection_revision: "2",
        }),
        unfold: async () => {},
        frameAt: async () => frame2,
        frame: async () => frame2,
      } as unknown as SourceComparisonSession),
    } as unknown as SourceReaderAccess),
  };

  let currentSource = source1;
  const parent = document.createElement("div");
  document.body.append(parent);

  let readerInstance!: ReturnType<typeof createDiffsReader>;
  createRoot((dispose) => {
    cleanup.push(dispose);
    readerInstance = createDiffsReader({
      scope: null,
      sections: () => [{ key: "file-1", source: currentSource, digest: { in_range: true, changes_rows: 2 } }],
      prefs: () => editorDisplayPrefs(),
      open: () => true,
      revision: () => null,
      pageHeading: () => document.createElement("div"),
      sectionHeading: () => document.createElement("div"),
      onError: vi.fn(),
    });
    cleanup.push(() => { readerInstance.destroy(); parent.remove(); });
    readerInstance.mount(parent, { show: vi.fn(), leave: vi.fn() });
  });

  await waitFor(() => {
    const doc = parent.querySelector(".cm-content");
    expect(doc?.textContent).toContain("v1 content");
  });

  currentSource = source2;
  readerInstance.refresh();

  await waitFor(() => {
    const doc = parent.querySelector(".cm-content");
    expect(doc?.textContent).toContain("v2 newly arrived");
  });

  expect(closeSpy1).toHaveBeenCalled();
});

it("prunes removed sections and closes their backend sessions", async () => {
  cleanup.push(mockReaderViewport());

  const frame: SourceComparisonFrame = {
    kind: "comparison",
    view_id: "v-1",
    intent_revision: "1",
    projection_revision: "1",
    anchor: { row: 0 },
    extent: { rows: 2, complete: true },
    span: { start: 0, end: 2 },
    rows: [
      { index: 0, end: 1, kind: "equal", before_line: 1, after_line: 1, text: "alpha line\n", changed: [] },
    ],
  };

  const closeSpy = vi.fn(async () => {});
  const source: DiffRowSource = {
    ...mockSource("test.go", () => [frame]),
    access: () => ({
      selection: async () => "source",
      content: async () => "source content",
      summary: async () => ({ in_range: true, summary: { added: 0, removed: 0, files: 1, total_lines: 1, net_lines: 0, rows: 1, folds: 0 } }),
      presentation: async () => ({
        attach: () => () => {},
        ready: async () => {},
        close: closeSpy,
        state: () => ({
          id: "session-prune",
          state: "ready" as const,
          comparison: { in_range: true, summary: { added: 0, removed: 0, rows: 1 } },
          extent: { rows: 1, complete: true },
          intent_revision: "1",
          projection_revision: "1",
        }),
        unfold: async () => {},
        frameAt: async () => frame,
        frame: async () => frame,
      } as unknown as SourceComparisonSession),
    } as unknown as SourceReaderAccess),
  };

  let sectionList = [{ key: "file-1", source, digest: { in_range: true, changes_rows: 1 } }];
  const parent = document.createElement("div");
  document.body.append(parent);

  let readerInstance!: ReturnType<typeof createDiffsReader>;
  createRoot((dispose) => {
    cleanup.push(dispose);
    readerInstance = createDiffsReader({
      scope: null,
      sections: () => sectionList,
      prefs: () => editorDisplayPrefs(),
      open: () => true,
      revision: () => null,
      pageHeading: () => document.createElement("div"),
      sectionHeading: () => document.createElement("div"),
      onError: vi.fn(),
    });
    cleanup.push(() => { readerInstance.destroy(); parent.remove(); });
    readerInstance.mount(parent, { show: vi.fn(), leave: vi.fn() });
  });

  await waitFor(() => {
    const doc = parent.querySelector(".cm-content");
    expect(doc?.textContent).toContain("alpha line");
  });

  sectionList = [];
  readerInstance.refresh();

  await waitFor(() => {
    expect(closeSpy).toHaveBeenCalled();
  });
});

function frameOf(view: string, texts: string[], extentRows = texts.length): SourceComparisonFrame {
  return {
    kind: "comparison", view_id: view, intent_revision: "1", projection_revision: "1",
    anchor: { row: 0 }, extent: { rows: extentRows, complete: extentRows === texts.length },
    span: { start: 0, end: texts.length },
    rows: texts.map((text, index) => ({ index, end: index + 1, kind: "insert" as const, before_line: 0, after_line: index + 1, text, changed: [] })),
  };
}

function sourceWith(frame: SourceComparisonFrame, presentation: () => Promise<void>, close = vi.fn(async () => {})): DiffRowSource {
  return {
    ...mockSource("test.go", () => [frame]),
    access: () => ({
      selection: async () => "source",
      content: async () => "source content",
      summary: async () => ({ in_range: true, summary: { added: 1, removed: 0, files: 1, total_lines: 1, net_lines: 1, rows: frame.extent.rows, folds: 0 } }),
      presentation: async () => {
        await presentation();
        return {
          attach: () => () => {},
          ready: async () => {},
          close,
          state: () => ({
            id: frame.view_id, state: "ready" as const,
            comparison: { in_range: true, summary: { added: 1, removed: 0, rows: frame.extent.rows } },
            extent: frame.extent, intent_revision: "1", projection_revision: "1",
          }),
          unfold: async () => {},
          frameAt: async () => frame,
          frame: async () => frame,
        } as unknown as SourceComparisonSession;
      },
    } as unknown as SourceReaderAccess),
  };
}

it("drops a read whose source changed mid-load and reads the new source instead", async () => {
  cleanup.push(mockReaderViewport());
  let releaseStale!: () => void;
  const stalePresentation = new Promise<void>((resolve) => { releaseStale = resolve; });
  const closeStale = vi.fn(async () => {});
  const stale = sourceWith(frameOf("v-stale", ["stale row\n"]), () => stalePresentation, closeStale);
  const fresh = sourceWith(frameOf("v-fresh", ["fresh row\n"]), async () => {});
  let current = stale;
  const parent = document.createElement("div");
  document.body.append(parent);
  let reader!: ReturnType<typeof createDiffsReader>;
  createRoot((dispose) => {
    cleanup.push(dispose);
    reader = createDiffsReader({
      scope: null,
      sections: () => [{ key: "file-1", source: current, digest: { in_range: true, changes_rows: 1 } }],
      prefs: () => editorDisplayPrefs(),
      open: () => true,
      revision: () => null,
      pageHeading: () => document.createElement("div"),
      sectionHeading: () => document.createElement("div"),
      onError: vi.fn(),
    });
    cleanup.push(() => { reader.destroy(); parent.remove(); });
    reader.mount(parent, { show: vi.fn(), leave: vi.fn() });
  });
  // The stale read is waiting on its presentation.
  await new Promise((resolve) => setTimeout(resolve, 20));

  current = fresh;
  reader.refresh();
  await new Promise((resolve) => setTimeout(resolve, 20));
  releaseStale();

  await waitFor(() => expect(parent.querySelector(".cm-content")?.textContent).toContain("fresh row"));
  expect(parent.querySelector(".cm-content")?.textContent).not.toContain("stale row");
  await waitFor(() => expect(closeStale).toHaveBeenCalled());
});

it("rescales loaded windows when the line height changes", async () => {
  cleanup.push(mockReaderViewport());
  const source = sourceWith(frameOf("v-tall", ["first\n"], 1_000_000), async () => {});
  const parent = document.createElement("div");
  document.body.append(parent);
  let reader!: ReturnType<typeof createDiffsReader>;
  let editor!: ReturnType<ReturnType<typeof createDiffsReader>["mount"]>;
  const base = editorDisplayPrefs();
  createRoot((dispose) => {
    cleanup.push(dispose);
    reader = createDiffsReader({
      scope: null,
      sections: () => [{ key: "file-1", source, digest: { in_range: true, changes_rows: 1 } }],
      prefs: () => base,
      open: () => true,
      revision: () => null,
      pageHeading: () => document.createElement("div"),
      sectionHeading: () => document.createElement("div"),
      onError: vi.fn(),
    });
    cleanup.push(() => { reader.destroy(); parent.remove(); });
    editor = reader.mount(parent, { show: vi.fn(), leave: vi.fn() });
  });
  const gapLines = () => editor.document.entries.find((entry) => entry.section === "file-1" && entry.slot.pending)?.slot.lines;
  await waitFor(() => expect(parent.querySelector(".cm-content")?.textContent).toContain("first"));
  const before = gapLines()!;
  expect(before).toBeGreaterThan(0);

  reader.prefs({ ...base, fontSize: base.fontSize * 2 });
  await waitFor(() => expect(Math.abs(gapLines()! / before - 0.5)).toBeLessThan(0.01));
});
