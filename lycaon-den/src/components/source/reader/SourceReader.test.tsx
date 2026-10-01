import { render, screen, waitFor, fireEvent, cleanup } from "@solidjs/testing-library";
import { createSignal, type Setter } from "solid-js";
import { afterEach, expect, it, vi } from "vitest";
import type { SourceComparisonFrame, SourceReaderRow } from "../../../api/types.ts";
import { EditorView } from "@codemirror/view";
import { Chunk } from "@codemirror/merge";
import { SourceReader, type SourceReaderHandle } from "./SourceReader.tsx";
import { sourceReaderAccess, type SourceReaderAccess } from "../../../api/source-reader.ts";
import { sourceReaderFixture } from "../../../test/source-reader-fixture.ts";
import { stubClient } from "../../../test/client-fixture.ts";
import { createNoticeStore, registerNoticePublisher } from "../../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../../notices/notice-select.ts";
import { receiveSourceViewEvent } from "../../../ui/paged-view/source-view-session.ts";
import { mockReaderViewport } from "../../../test/reader-viewport-mock.ts";

afterEach(() => { registerNoticePublisher(null); cleanup(); });

it("reports a failed comparison as a project notice and leaves the reader blank", async () => {
  const store = createNoticeStore();
  registerNoticePublisher(store);
  const value = fixture("a\n", "b\n");
  value.rows.mockRejectedValue(new Error("Rows unavailable"));
  render(() => <SourceReader projectId="project" access={value.access} path="example.ts" />);
  await waitFor(() => {
    const rows = selectProjectNoticeGroups(store.index()).find(group => group.projectId === "project")?.notices ?? [];
    expect(rows).toMatchObject([{ code: "source_comparison_unavailable", message: "Rows unavailable" }]);
  });
  expect(screen.queryByRole("alert")).toBeNull();
  expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
});

it("paints completed secret annotations without reopening the diff or losing selection", async () => {
  const value = fixture("token\n", "token\n");
  render(() => <SourceReader access={value.access} path="example.ts" />);
  await waitFor(() => expect(screen.getByText("token")).toBeTruthy());
  const editor = EditorView.findFromDOM(document.querySelector(".cm-editor")!)!;
  editor.dispatch({ selection: { anchor: 1, head: 4 } });
  const created = value.create.mock.calls.length;
  const rows: SourceReaderRow[] = [{ index: 0, end: 1, kind: "equal", text: "token\n", before_line: 1, after_line: 1, changed: [],
    secret_screen: { truncated: false, spans: [{ start: 0, end: 5, state: "detected", rule_id: "fixture", rule_title: "Fixture" }] } }];
  for (const view of value.host.publishRows(value.reference, rows)) {
    receiveSourceViewEvent({ kind: "comparison", view_id: view.id, intent_revision: view.intent_revision,
      projection_revision: view.projection_revision, invalidated: false, terminal: true });
  }
  await waitFor(() => expect(document.querySelector(".cm-den-secret--detected")?.textContent).toBe("token"));
  expect(value.create).toHaveBeenCalledTimes(created);
  expect(editor.state.sliceDoc(editor.state.selection.main.from, editor.state.selection.main.to)).toBe("oke");
  expect(value.rows.mock.calls.every(call => (call[2].limit ?? 200) <= 200)).toBe(true);
});

function fixture(before: string | null, after: string | null) {
  const host = sourceReaderFixture();
  const reference = host.prepare(before, after, "example.ts");
  const rows = vi.fn(host.getSourceViewRows), updates = vi.fn(host.applySourceViewIntent), create = vi.fn(host.createSourceView);
  const client = stubClient({ ...host.methods, getSourceViewRows: rows, applySourceViewIntent: updates, createSourceView: create });
  return { access: sourceReaderAccess(client, "project", reference), rows, updates, create, reference, host, client };
}

it("remounts an inline comparison with its loaded rows while the new presentation waits", async () => {
  const value = fixture("old\n", Array.from({ length: 80 }, (_, i) => `changed ${i}\n`).join(""));
  const mounted = render(() => <SourceReader access={value.access} path="example.ts" scrollPastEnd={false} />);
  await waitFor(() => expect(screen.getByText("changed 0")).toBeTruthy());
  const editor = () => EditorView.findFromDOM(document.querySelector(".cm-editor")!)!;
  const previous = editor().state.doc.toString();
  mounted.unmount();
  const presentation = value.access.presentation.bind(value.access);
  let resume!: () => void;
  const pending = new Promise<void>(resolve => { resume = resolve; });
  vi.spyOn(value.access, "presentation").mockImplementation(async (...args) => { await pending; return presentation(...args); });
  render(() => <SourceReader access={value.access} path="example.ts" scrollPastEnd={false} />);
  expect(editor().state.doc.toString()).toBe(previous);
  await Promise.resolve();
  expect(editor().state.doc.toString()).toBe(previous);
  resume();
  await waitFor(() => expect(value.rows).toHaveBeenCalledTimes(2));
  expect(editor().state.doc.toString()).toBe(previous);
});

it("does not reuse an inline window for another comparison or presentation mode", async () => {
  const value = fixture("old\n", "changed\n");
  const mounted = render(() => <SourceReader access={value.access} path="example.ts" scrollPastEnd={false} />);
  await waitFor(() => expect(screen.getByText("changed")).toBeTruthy());
  mounted.unmount();
  const other = fixture("before\n", "other\n");
  other.access.presentation = () => new Promise(() => {});
  const next = render(() => <SourceReader access={other.access} path="example.ts" scrollPastEnd={false} />);
  expect(screen.queryByText("changed")).toBeNull();
  next.unmount();
  value.access.presentation = () => new Promise(() => {});
  render(() => <SourceReader access={value.access} path="example.ts" current scrollPastEnd={false} />);
  expect(screen.queryByText("changed")).toBeNull();
});

it("uses bounded host frames for changes and full-file presentation", async () => {
  const diff = vi.spyOn(Chunk, "build");
  const source = Array.from({ length: 2000 }, (_, i) => `line ${i}\n`).join("");
  const value = fixture(source, source.replace("line 1000\n", "changed\n"));
  render(() => <SourceReader access={value.access} path="example.ts" />);
  await waitFor(() => expect(value.rows).toHaveBeenCalledTimes(1));
  expect(value.rows.mock.calls[0]?.[2]).toMatchObject({ offset: 0, limit: 200 });
  expect(value.rows.mock.calls[0]?.[2]).not.toHaveProperty("layout");
  const view = EditorView.findFromDOM(document.querySelector(".cm-editor")!)!;
  expect(view.state.readOnly).toBe(true);
  expect(view.state.doc.length).toBeLessThan(500);
  expect(diff).not.toHaveBeenCalled(); diff.mockRestore();
  fireEvent.click(screen.getByText("Full file"));
  await waitFor(() => expect(value.updates.mock.calls.some(call => call[2].kind === "comparison" && call[2].intent.mode === "full")).toBe(true));
  await waitFor(() => expect(value.rows.mock.calls.some(call => call[2].offset === 200)).toBe(true));
  expect(view.state.doc.length).toBeLessThan(8000);
  expect(value.rows.mock.calls.every(call => (call[2].limit ?? 200) <= 200)).toBe(true);
});

it("ignores a late frame from the previous selected comparison", async () => {
  const first = fixture("old\n", "first\n"), second = fixture("old\n", "selected\n");
  let release!: () => void;
  const held = new Promise<void>(resolve => { release = resolve; });
  const original = first.rows.getMockImplementation()!;
  first.rows.mockImplementation(async (...args) => { await held; return original(...args); });
  let select!: Setter<SourceReaderAccess>;
  render(() => { const [access, setAccess] = createSignal(first.access); select = setAccess; return <SourceReader access={access()} path="example.ts" />; });
  await waitFor(() => expect(first.rows).toHaveBeenCalled());
  select(second.access);
  await waitFor(() => expect(screen.getByText(/selected/)).toBeTruthy());
  release(); await Promise.resolve();
  expect(screen.queryByText(/^first/)).toBeNull();
  expect(screen.getByText(/selected/)).toBeTruthy();
});

it("holds the outgoing comparison until its replacement's first window lands", async () => {
  const first = fixture("old\n", "first\n"), second = fixture("old\n", "selected\n");
  let release!: () => void;
  const held = new Promise<void>(resolve => { release = resolve; });
  const original = second.rows.getMockImplementation()!;
  second.rows.mockImplementation(async (...args) => { await held; return original(...args); });
  let select!: Setter<SourceReaderAccess>;
  render(() => { const [access, setAccess] = createSignal(first.access); select = setAccess; return <SourceReader access={access()} path="example.ts" />; });
  await waitFor(() => expect(screen.getByText(/first/)).toBeTruthy());

  select(second.access);
  // The outgoing rows stay while the replacement loads.
  await waitFor(() => expect(second.rows).toHaveBeenCalled());
  expect(screen.getByText(/first/)).toBeTruthy();

  release();
  await waitFor(() => expect(screen.getByText(/selected/)).toBeTruthy());
  expect(screen.queryByText(/^first/)).toBeNull();
});

it("drops the held comparison when its replacement has nothing to show", async () => {
  const first = fixture("old\n", "first\n"), empty = fixture(null, null);
  let select!: Setter<SourceReaderAccess>;
  render(() => { const [access, setAccess] = createSignal(first.access); select = setAccess; return <SourceReader access={access()} path="example.ts" />; });
  await waitFor(() => expect(screen.getByText(/first/)).toBeTruthy());

  select(empty.access);

  await waitFor(() => expect(screen.queryByText(/^first/)).toBeNull());
});

it("expands only the chosen unchanged range through host intent", async () => {
  const source = Array.from({ length: 1000 }, (_, i) => `line ${i}\n`).join("");
  const value = fixture(source, source.replace("line 500\n", "changed\n"));
  render(() => <SourceReader access={value.access} path="example.ts" />);
  await waitFor(() => expect(screen.getAllByText("Show lines")).toHaveLength(2));
  fireEvent.click(screen.getAllByText("Show lines")[0]!);
  await waitFor(() => expect(value.updates).toHaveBeenCalledOnce());
  expect(value.updates.mock.calls[0]?.[2]).toMatchObject({ kind: "comparison", intent: { mode: "changes", expanded: [{ start: 0, end: 497 }] } });
  await waitFor(() => expect(screen.getByText(/line 0$/)).toBeTruthy());
  const created = value.create.mock.calls.length;
  const editor = EditorView.findFromDOM(document.querySelector(".cm-editor")!)!;
  editor.dispatch({ selection: { anchor: 1, head: 5 } });
  fireEvent.click(screen.getByText("Full file"));
  await waitFor(() => expect(value.updates.mock.calls.at(-1)?.[2]).toMatchObject({ intent: { mode: "full", expanded: [{ start: 0, end: 497 }] } }));
  fireEvent.click(screen.getByText("Changes"));
  await waitFor(() => expect(value.updates.mock.calls.at(-1)?.[2]).toMatchObject({ intent: { mode: "changes", expanded: [{ start: 0, end: 497 }] } }));
  await waitFor(() => expect(editor.state.sliceDoc(editor.state.selection.main.from, editor.state.selection.main.to)).toBe("ine "));
  expect(value.create).toHaveBeenCalledTimes(created);
});

it("find loads and selects a match across source fragments", async () => {
  const value = fixture("abcdef\n", "abcdef\n");
  const fragments: SourceReaderRow[] = ["ab", "cd", "ef\n"].map((text, index) => ({ index, end: index + 1,
    kind: "equal", text, before_line: 1, after_line: 1, column: index * 2, changed: [] }));
  value.host.setRows(value.reference, fragments);
  value.rows.mockImplementationOnce(async (...args) => {
    const frame = await value.host.getSourceViewRows(...args) as SourceComparisonFrame;
    return { ...frame, rows: fragments.slice(0, 1), span: { start: 0, end: 1 } };
  });
  let handle: SourceReaderHandle | undefined;
  render(() => <SourceReader path="example.ts" access={value.access} onHandle={value => { handle = value; }} />);
  await waitFor(() => expect(value.rows).toHaveBeenCalled());
  await handle?.revealRow(0, { row: 0, from: 1, to: 5 });
  const view = EditorView.findFromDOM(document.querySelector(".cm-editor")!)!;
  expect(view.state.doc.toString()).toBe("abcdef\n");
  expect(view.state.sliceDoc(view.state.selection.main.from, view.state.selection.main.to)).toBe("bcde");
});

it("keeps change shading continuous through host syntax tokens", async () => {
  const value = fixture("old\n", "const café = value;\n");
  const text = "const café = value;\n";
  const row: SourceReaderRow = { index: 0, end: 1, kind: "insert", text, before_line: 0, after_line: 1,
    changed: [{ from: 0, to: 10 }], syntax: [{ from: 0, to: 5, kind: "keyword" }, { from: 6, to: 10, kind: "variable" }] };
  value.host.setRows(value.reference, [row]);
  render(() => <SourceReader access={value.access} path="example.ts" />);
  await waitFor(() => expect(document.querySelector(".cm-changedText")?.textContent).toBe("const café"));
  expect(document.querySelectorAll(".cm-changedText")).toHaveLength(1);
  expect(document.querySelector(".cm-changedText")?.children.length).toBeGreaterThan(0);
});

it("reads an added file as the file itself, with nothing to fold or split", async () => {
  const value = fixture(null, "one\ntwo\n");
  render(() => <SourceReader access={value.access} path="example.ts" split />);
  await waitFor(() => expect(document.querySelectorAll(".cm-den-reader [data-source-row]")).toHaveLength(2));
  expect(document.querySelector(".cm-den-reader-add, .cm-den-reader-delete")).toBeNull();
  expect(document.querySelectorAll(".cm-editor")).toHaveLength(1);
  expect(screen.queryByText("Full file")).toBeNull();
  expect(value.create.mock.calls.every(call => call[1].intent && !("mode" in call[1].intent && call[1].intent.mode === "split"))).toBe(true);
});

it("keeps a deleted file's last contents marked as removed and numbered by them", async () => {
  const value = fixture("one\ntwo\nthree\n", null);
  let handle: SourceReaderHandle | undefined;
  render(() => <SourceReader access={value.access} path="example.ts" split onHandle={next => { handle = next; }} />);
  await waitFor(() => expect(document.querySelectorAll(".cm-den-reader-delete")).toHaveLength(3));
  expect(document.querySelectorAll(".cm-editor")).toHaveLength(1);
  expect(screen.queryByText("Full file")).toBeNull();
  expect(await handle!.content()).toBe("one\ntwo\nthree\n");
});

it("says so when neither side of the comparison has a file", async () => {
  const value = fixture(null, null);
  render(() => <SourceReader access={value.access} path="example.ts" />);
  await waitFor(() => expect(screen.getByRole("status").textContent).toMatch(/either side/));
  expect(screen.getByLabelText("Contents of example.ts").hidden).toBe(true);
  expect(value.rows).not.toHaveBeenCalled();
});

it("places a changed file's sides together when split is requested", async () => {
  const value = fixture("one\n", "two\n");
  render(() => <SourceReader access={value.access} path="example.ts" split />);
  await waitFor(() => expect(value.rows).toHaveBeenCalled());
  expect(value.create.mock.calls.at(-1)?.[1]).toMatchObject({ intent: { mode: "split" } });
  expect(document.querySelectorAll(".cm-editor")).toHaveLength(2);
});

it("keeps a sliding window however large the comparison is", async () => {
  const source = Array.from({ length: 2500 }, (_, i) => `line ${i}\n`).join("");
  const value = fixture(null, source);
  render(() => <SourceReader access={value.access} path="example.ts" />);
  await waitFor(() => expect(value.rows).toHaveBeenCalled());
  expect(value.rows.mock.calls.length).toBeLessThanOrEqual(3);
});

it("reserves an inline reader's own rows, so the reserve shrinks when they do", async () => {
  const restoreViewport = mockReaderViewport();
  let rowsHeight = 500;
  const measured = vi.spyOn(EditorView.prototype, "contentHeight", "get").mockImplementation(() => rowsHeight);
  try {
    const value = fixture("old\n", "new\n");
    render(() => <SourceReader access={value.access} path="example.ts" scrollPastEnd={false} />);
    const editors = () => document.querySelector<HTMLElement>(".den-source-reader__editors")!;
    await waitFor(() => expect(editors().style.minHeight).toBe("500px"));
    expect(screen.getByTestId("source-reader").style.minHeight).toBe("");

    rowsHeight = 200;
    const view = EditorView.findFromDOM(document.querySelector(".cm-editor")!)!;
    view.dispatch({ changes: { from: 0, insert: " " } });
    await waitFor(() => expect(editors().style.minHeight).toBe("200px"));
  } finally {
    measured.mockRestore();
    restoreViewport();
  }
});
