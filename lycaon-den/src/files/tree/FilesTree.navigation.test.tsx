import { batch, createSignal } from "solid-js";
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, waitFor } from "@solidjs/testing-library";
import { FilesTree } from "./FilesTree.tsx";
import type { FilesTreeEntrySelection } from "./files-tree-context.ts";
import { treeRow, treeViewFixture, TREE_ROOTS } from "../../test/source-tree-view-fixture.ts";
import { resetFilesTreeViewStateForTests } from "./files-tree-view-state.ts";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import { flushScrollportFrameForTests } from "../../platform/scrolling/scrollport-frame.ts";

afterEach(() => { cleanup(); resetFilesTreeViewStateForTests(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

const rank = (path: string) => path === "." ? 0 : Number(path.slice(5, -3));
type Mode = "automatic" | "explicit";

async function mount(count = 200001) {
  const fixture = treeViewFixture();
  fixture.large(count, index => index === 0 ? treeRow(".", "directory", true) : treeRow(`file-${index}.ts`), rank);
  const [active, setActive] = createSignal<{ rootId: string; path: string; revision: number } | null>(null);
  const [revision, setRevision] = createSignal(0);
  const [visible, setVisible] = createSignal(true);
  const [selection, setSelection] = createSignal<FilesTreeEntrySelection | null>(null);
  let scroll!: HTMLDivElement;
  let reveal!: (root: string, path: string, dir: boolean) => void;
  const mounted = render(() => <div class="den-files-tree-scroll-frame">
    <div class="den-files-tree-scroll" ref={element => {
      scroll = element;
      Object.defineProperties(element, {
        clientHeight: { configurable: true, value: 300 },
        clientWidth: { configurable: true, value: 240 },
        scrollHeight: { configurable: true, get: () => Math.max(300,
          Number.parseFloat(element.querySelector<HTMLElement>(".den-files-tree-virtual")?.style.height ?? "0")) },
      });
    }}>
      <FilesTree projectId="navigation" workspaceId="ws1" client={fixture.client} roots={TREE_ROOTS}
        visible={visible()} navigationRevision={revision()} navigationOrigin="reader" activeFile={active()}
        selectedEntry={selection()} onSelectEntry={setSelection} onOpenFile={() => {}}
        createEntry={async (_kind, _root, path) => path} onReady={tree => { reveal = tree.revealPath; }} />
    </div><div data-files-tree-sticky-host /></div>);
  await waitFor(() => expect(scroll.querySelector('[data-path="file-1.ts"]')).not.toBeNull());
  const navigate = (mode: Mode, row: number) => {
    if (mode === "explicit") reveal("r1", `file-${row}.ts`, false);
    else batch(() => {
      const next = revision() + 1;
      setRevision(next); setActive({ rootId: "r1", path: `file-${row}.ts`, revision: next });
    });
  };
  return { ...mounted, fixture, scroll, navigate, selection, setVisible, setRevision, setActive };
}

function paintedRows(scroll: HTMLElement) {
  flushScrollportFrameForTests(scroll);
  const inner = scroll.querySelector<HTMLElement>(".den-files-tree-virtual__inner")!;
  const shift = Number.parseFloat(inner.style.transform.slice("translateY(".length)) || 0;
  return [...scroll.querySelectorAll<HTMLElement>(".den-files-tree-virtual__row")].flatMap(row => {
    const top = shift + Number.parseFloat(row.style.transform.slice("translateY(".length)) - scroll.scrollTop;
    const path = row.querySelector<HTMLElement>("[data-path]")?.dataset.path;
    return path && top + 26 > 0 && top < 300 ? [{ index: rank(path), top }] : [];
  }).sort((a, b) => a.top - b.top);
}

async function recordReveal(view: Awaited<ReturnType<typeof mount>>, mode: Mode, target: number) {
  const samples: ReturnType<typeof paintedRows>[] = [];
  let frame = 0;
  const sample = () => { samples.push(paintedRows(view.scroll)); frame = requestAnimationFrame(sample); };
  sample();
  try {
    view.navigate(mode, target);
    await waitFor(() => expect(paintedRows(view.scroll).some(row => row.index === target)).toBe(true), { timeout: 5000 });
    // Observe completion, including any delayed scroll or anchor restoration.
    await new Promise(resolve => setTimeout(resolve, 100));
  } finally { cancelAnimationFrame(frame); }
  return samples;
}

it.each(["automatic", "explicit"] as const)("paints every %s reveal frame across large-tree segments in both directions", async mode => {
  const view = await mount();
  view.fixture.read(async () => { await new Promise(resolve => setTimeout(resolve, 20)); });
  for (const target of [170000, 50]) {
    const samples = await recordReveal(view, mode, target);
    expect(samples.filter(rows => rows.length === 0)).toEqual([]);
    for (const rows of samples) {
      expect(rows[0]!.top).toBeLessThanOrEqual(0);
      expect(rows.at(-1)!.top + 26).toBeGreaterThanOrEqual(300);
      expect(rows.every((row, index) => index === 0 || row.index === rows[index - 1]!.index + 1)).toBe(true);
    }
    const positions = samples.map(rows => rows[0]!.index);
    expect(new Set(positions).size).toBeGreaterThan(4);
    const direction = target === 170000 ? 1 : -1;
    expect(positions.every((position, index) => index === 0 || direction * (position - positions[index - 1]!) >= 0)).toBe(true);
    expect(view.scroll.scrollHeight).toBe(4_000_000);
  }
  const reads = view.fixture.requests.filter(request => request.path.includes("/rows?"));
  expect(reads.length).toBeLessThan(100);
  expect(reads.every(request => Number(new URL(request.path, "http://fixture").searchParams.get("limit")) <= 200)).toBe(true);
});

it.each([50, 1500])("retains smooth motion for a smaller tree to row %s", async target => {
  const view = await mount(2001);
  const samples = await recordReveal(view, "automatic", target);
  expect(samples.every(rows => rows.length > 0)).toBe(true);
  expect(new Set(samples.map(rows => rows[0]!.index)).size).toBeGreaterThan(4);
});

it.each(["automatic", "explicit"] as const)("cancels a pending %s reveal when the reader takes control", async mode => {
  const view = await mount();
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  let reading = false;
  view.fixture.read(async () => { reading = true; await gate; });
  view.navigate(mode, 170000);
  await waitFor(() => expect(reading).toBe(true));
  fireEvent.pointerDown(view.scroll);
  const position = view.scroll.scrollTop;
  release();
  await new Promise(resolve => setTimeout(resolve, 400));
  expect(view.scroll.scrollTop).toBe(position);
  expect(paintedRows(view.scroll)[0]!.index).toBe(0);
  expect(view.selection()).toBeNull();
});

it("supersedes a pending reveal without letting its late response move or focus the tree", async () => {
  const view = await mount();
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  let reading = false;
  view.fixture.read(async () => { reading = true; await gate; });
  view.navigate("explicit", 170000);
  await waitFor(() => expect(reading).toBe(true));
  view.fixture.read(undefined);
  const next = recordReveal(view, "automatic", 50);
  release();
  await next;
  expect(paintedRows(view.scroll).some(row => row.index === 50)).toBe(true);
  expect(view.selection()).toBeNull();
  expect(document.activeElement?.getAttribute("data-path")).not.toBe("file-170000.ts");
});

it("publishes only the prepared destination under reduced motion", async () => {
  const view = await mount();
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: query.includes("prefers-reduced-motion") }));
  const samples = await recordReveal(view, "explicit", 170000);
  expect(samples.every(rows => rows.length > 0)).toBe(true);
  expect(new Set(samples.map(rows => rows[0]!.index)).size).toBe(2);
  expect(view.selection()?.path).toBe("file-170000.ts");
  expect(document.activeElement?.getAttribute("data-path")).toBe("file-170000.ts");
});

it.each(["hide", "unmount"] as const)("abandons an outstanding reveal on %s", async action => {
  const view = await mount();
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  let reading = false;
  view.fixture.read(async () => { reading = true; await gate; });
  view.navigate("explicit", 170000);
  await waitFor(() => expect(reading).toBe(true));
  if (action === "hide") view.setVisible(false); else view.unmount();
  release();
  await new Promise(resolve => setTimeout(resolve, 300));
  expect(view.scroll.scrollTop).toBe(0);
  expect(view.selection()).toBeNull();
});

it("re-reveals the same active file after reader input interrupted its first attempt", async () => {
  const view = await mount();
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  let reading = false;
  view.fixture.read(async () => { reading = true; await gate; });
  view.navigate("automatic", 170000);
  await waitFor(() => expect(reading).toBe(true));
  fireEvent.pointerDown(view.scroll);
  view.fixture.read(undefined); release();
  const samples = await recordReveal(view, "automatic", 170000);
  expect(samples.every(rows => rows.length > 0)).toBe(true);
  expect(paintedRows(view.scroll).some(row => row.index === 170000)).toBe(true);
});

it("keeps keyboard navigation when an earlier editor target arrives late", async () => {
  const view = await mount();
  view.setRevision(1);
  const first = view.scroll.querySelector<HTMLButtonElement>('button[data-path="file-1.ts"]')!;
  first.focus();
  expect(document.activeElement).toBe(first);
  fireEvent.keyDown(first, { key: "End" });
  await waitFor(() => expect(document.activeElement?.getAttribute("data-path")).toBe("file-200000.ts"), { timeout: 5000 });
  view.setActive({ rootId: "r1", path: "file-1.ts", revision: 1 });
  await new Promise(resolve => setTimeout(resolve, 400));
  expect(paintedRows(view.scroll).some(row => row.index === 200000)).toBe(true);
  expect(document.activeElement?.getAttribute("data-path")).toBe("file-200000.ts");
  await recordReveal(view, "automatic", 50);
  expect(paintedRows(view.scroll).some(row => row.index === 50)).toBe(true);
});

it("keeps an interrupted animation's prepared viewport visible when a page read fails", async () => {
  const notices = createNoticeStore();
  registerNoticePublisher(notices);
  const view = await mount();
  const logged = vi.spyOn(console, "error").mockImplementation(() => {});
  try {
    let reads = 0;
    view.fixture.read(async () => { if (++reads === 6) throw new Error("Navigation page unavailable"); });
    view.navigate("automatic", 170000);
    await waitFor(() => expect(selectProjectNoticeGroups(notices.index()).find(group => group.projectId === "navigation")?.notices).toHaveLength(1));
    expect(paintedRows(view.scroll).length).toBeGreaterThan(0);
  } finally { logged.mockRestore(); registerNoticePublisher(null); }
});


it.each(["automatic", "explicit"] as const)("starts a resident %s reveal without a host round trip", async mode => {
  const view = await mount(2001);
  const before = view.fixture.requests.length;
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  view.fixture.read(() => gate);
  try {
    await recordReveal(view, mode, 50);
    expect(view.fixture.commands).toEqual([]);
    // Background overscan may read ahead, but reveal completes while every read is blocked.
    expect(view.fixture.requests.slice(before).filter(request => request.path.includes("anchor="))).toEqual([]);
  } finally { release(); }
});

it("locates an uncached expanded target without rebuilding or rebasing the presentation", async () => {
  const view = await mount();
  const before = view.fixture.requests.length;
  await recordReveal(view, "automatic", 170000);
  expect(view.fixture.commands).toEqual([]);
  const reads = view.fixture.requests.slice(before).filter(request => request.path.includes("/rows?"));
  const anchored = reads.filter(request => new URL(request.path, "http://fixture").searchParams.has("anchor"));
  expect(anchored).toHaveLength(1);
});

it("animates scroll to center a newly revealed file when disclosing a closed folder", async () => {
  const fixture = treeViewFixture([
    treeRow(".", "directory", true),
    ...Array.from({ length: 20 }, (_, i) => treeRow(`top-${i}.ts`)),
    treeRow("folder", "directory", false),
    treeRow("folder/target.ts"),
    ...Array.from({ length: 30 }, (_, i) => treeRow(`bottom-${i}.ts`)),
  ]);
  const [active] = createSignal<{ rootId: string; path: string; revision: number } | null>(null);
  const [revision] = createSignal(0);
  let scroll!: HTMLDivElement;
  let reveal!: (root: string, path: string, dir: boolean) => void;
  render(() => <div class="den-files-tree-scroll-frame">
    <div class="den-files-tree-scroll" ref={element => {
      scroll = element;
      Object.defineProperties(element, {
        clientHeight: { configurable: true, value: 260 },
        clientWidth: { configurable: true, value: 240 },
        scrollHeight: { configurable: true, get: () => Math.max(260,
          Number.parseFloat(element.querySelector<HTMLElement>(".den-files-tree-virtual")?.style.height ?? "0")) },
      });
    }}>
      <FilesTree projectId="navigation" workspaceId="ws1" client={fixture.client} roots={TREE_ROOTS}
        visible={true} navigationRevision={revision()} navigationOrigin="reader" activeFile={active()}
        selectedEntry={null} onSelectEntry={() => {}} onOpenFile={() => {}}
        createEntry={async (_kind, _root, path) => path} onReady={tree => { reveal = tree.revealPath; }} />
    </div><div data-files-tree-sticky-host /></div>);
  await waitFor(() => expect(scroll.querySelector('[data-path="top-1.ts"]')).not.toBeNull());
  expect(scroll.scrollTop).toBe(0);

  const samples: number[] = [];
  let frame = 0;
  const sample = () => { samples.push(scroll.scrollTop); frame = requestAnimationFrame(sample); };
  sample();
  try {
    reveal("r1", "folder/target.ts", false);
    await waitFor(() => expect(scroll.querySelector('[data-path="folder/target.ts"]')).not.toBeNull(), { timeout: 5000 });
    await waitFor(() => expect(scroll.scrollTop).toBeGreaterThan(0), { timeout: 5000 });
    await new Promise(resolve => setTimeout(resolve, 150));
  } finally { cancelAnimationFrame(frame); }
  expect(samples.filter(s => s > 0).length).toBeGreaterThan(3);
});
