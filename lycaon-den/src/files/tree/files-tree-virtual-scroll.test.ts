// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { bindScrollportMotion, unbindScrollportMotion } from "../../platform/scrolling/scrollport-motion.ts";
import { flushScrollportFrameForTests } from "../../platform/scrolling/scrollport-frame.ts";
import {
  fixedVirtualRows,
  fixedVirtualWindow,
  observeVirtualScrollOffset,
  renderWindowRows,
  scrollTopToAlignRowAtStart,
  scrollTopToAlignRowCentered,
  scrollTopToRevealRow,
  windowCoversRows,
} from "./files-tree-virtual-scroll.ts";

afterEach(() => vi.unstubAllGlobals());

describe("observeVirtualScrollOffset", () => {
  it("replaces a queued native sample when a segment move commits before paint", () => {
    const el = document.createElement("div");
    Object.defineProperties(el, {
      clientHeight: { value: 500 }, scrollHeight: { value: 10_000 },
    });
    const motion = bindScrollportMotion(el, el, el);
    const notify = vi.fn();
    const stop = observeVirtualScrollOffset(el, notify);
    el.scrollTop = 3_000;
    el.dispatchEvent(new Event("scroll"));
    motion.commit(1_200, "restore_anchor");
    flushScrollportFrameForTests(el);
    expect(notify).toHaveBeenLastCalledWith(1_200, true);
    expect(notify.mock.calls.some(([offset]) => offset === 3_000)).toBe(false);
    stop();
    unbindScrollportMotion(el);
  });

  it("coalesces a scroll burst and reports the latest offset next frame", () => {
    const frames: FrameRequestCallback[] = [];
    const cancel = vi.fn();
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      frames.push(cb);
      return frames.length;
    });
    vi.stubGlobal("cancelAnimationFrame", cancel);
    const el = document.createElement("div");
    const notify = vi.fn();
    const stop = observeVirtualScrollOffset(el, notify);

    expect(notify).toHaveBeenCalledWith(0, false);
    el.scrollTop = 10;
    el.dispatchEvent(new Event("scroll"));
    el.scrollTop = 30;
    el.dispatchEvent(new Event("scroll"));

    expect(frames).toHaveLength(1);
    expect(notify).toHaveBeenCalledTimes(1);
    frames[0]!(0);
    expect(notify).toHaveBeenLastCalledWith(30, true);

    el.scrollTop = 40;
    el.dispatchEvent(new Event("scroll"));
    stop();
    expect(cancel).toHaveBeenCalledWith(2);
  });
});

describe("windowCoversRows", () => {
  it("covers a visible range only when every row has a slot", () => {
    const window = { startIndex: 10, endIndex: 40 };
    expect(windowCoversRows(window, 10, 41)).toBe(true);
    expect(windowCoversRows(window, 12, 30)).toBe(true);
    expect(windowCoversRows(window, 9, 30)).toBe(false);
    expect(windowCoversRows(window, 20, 42)).toBe(false);
    expect(windowCoversRows(null, 0, 1)).toBe(false);
  });
});

describe("renderWindowRows", () => {
  const positions = fixedVirtualRows({ startIndex: 3, endIndex: 5 }, 26);
  const identity = (row: string) => row;
  const loaded = new Map(["c", "d", "e"].map((row, offset) => [row, { position: positions[offset]!, row }]));

  it("keeps the last row at a position whose page went missing while retaining", () => {
    const rows = renderWindowRows(positions, index => (index === 4 ? undefined : String.fromCharCode(96 + index)), identity, loaded, true);
    expect([...rows.keys()]).toEqual(["c", "e", "d"]);
    expect(rows.get("d")).toEqual({ position: positions[1], row: "d" });
  });

  it("drops missing positions once retention ends", () => {
    const rows = renderWindowRows(positions, index => (index === 4 ? undefined : String.fromCharCode(96 + index)), identity, loaded, false);
    expect([...rows.keys()]).toEqual(["c", "e"]);
  });

  it("never duplicates a row that moved to another position", () => {
    const rows = renderWindowRows(positions, index => (index === 3 ? "d" : undefined), identity, loaded, true);
    expect([...rows.entries()]).toEqual([
      ["d", { position: positions[0], row: "d" }],
      ["e", { position: positions[2], row: "e" }],
    ]);
  });

  it("retains nothing without a previous render", () => {
    const rows = renderWindowRows(positions, () => undefined, identity, undefined, true);
    expect(rows.size).toBe(0);
  });
});

describe("fixedVirtualRows", () => {
  it("allocates a viewport-sized runway for a million-row tree", () => {
    const window = fixedVirtualWindow({
      count: 1_000_000,
      scrollTop: 13_000_000,
      viewportHeight: 520,
      rowHeight: 26,
      bufferViewports: 1.5,
    });
    const rows = fixedVirtualRows(window, 26);

    expect(rows).toHaveLength(80);
    expect(rows[0]).toEqual({ index: 499_970, start: 12_999_220, size: 26 });
    expect(rows[rows.length - 1]).toEqual({
      index: 500_049,
      start: 13_001_274,
      size: 26,
    });
  });

  it("clamps the window at the list boundaries", () => {
    const window = fixedVirtualWindow({
      count: 3,
      scrollTop: -20,
      viewportHeight: 52,
      rowHeight: 26,
      bufferViewports: 1.5,
    });
    expect(fixedVirtualRows(window, 26).map((row) => row.index)).toEqual([0, 1, 2]);
    expect(
      fixedVirtualWindow({
        count: 0,
        scrollTop: 0,
        viewportHeight: 100,
        rowHeight: 26,
        bufferViewports: 1.5,
      }),
    ).toBeNull();
  });

  it("keeps the mounted window stable until the viewport reaches its guard", () => {
    const first = fixedVirtualWindow({
      count: 1_000,
      scrollTop: 2_600,
      viewportHeight: 520,
      rowHeight: 26,
      bufferViewports: 1.5,
    });
    const withinRunway = fixedVirtualWindow({
      count: 1_000,
      scrollTop: 2_860,
      viewportHeight: 520,
      rowHeight: 26,
      bufferViewports: 1.5,
      previous: first ?? undefined,
    });
    const pastGuard = fixedVirtualWindow({
      count: 1_000,
      scrollTop: 3_380,
      viewportHeight: 520,
      rowHeight: 26,
      bufferViewports: 1.5,
      previous: first ?? undefined,
    });

    expect(withinRunway).toBe(first);
    expect(pastGuard).not.toBe(first);
  });
});

describe("scrollTopToRevealRow", () => {
  const sticky = (scrollTop: number) => (scrollTop < 26 ? 26 : 52);

  it("returns null when the row is already below the sticky band", () => {
    expect(
      scrollTopToRevealRow({
        index: 4,
        rowHeight: 26,
        scrollTop: 0,
        viewportHeight: 260,
        contentHeight: 2600,
        stickyHeightAt: sticky,
      }),
    ).toBeNull();
  });

  it("scrolls just enough to bring a row up from below the fold", () => {
    expect(
      scrollTopToRevealRow({
        index: 20,
        rowHeight: 26,
        scrollTop: 0,
        viewportHeight: 260,
        contentHeight: 2600,
        stickyHeightAt: () => 0,
      }),
    ).toBe(20 * 26 + 26 - 260);
  });

  it("uses content height, not a live scrollHeight read, to clamp", () => {
    expect(
      scrollTopToRevealRow({
        index: 99,
        rowHeight: 26,
        scrollTop: 0,
        viewportHeight: 260,
        contentHeight: 100 * 26,
        stickyHeightAt: () => 0,
      }),
    ).toBe(100 * 26 - 260);
  });
});

describe("scrollTopToAlignRowAtStart", () => {
  it("moves an already visible row to the top below sticky ancestors", () => {
    expect(
      scrollTopToAlignRowAtStart({
        index: 8,
        rowHeight: 26,
        viewportHeight: 260,
        contentHeight: 2600,
        stickyHeightAt: () => 52,
      }),
    ).toBe(8 * 26 - 52);
  });

  it("clamps near the end when the row cannot reach the top", () => {
    expect(
      scrollTopToAlignRowAtStart({
        index: 99,
        rowHeight: 26,
        viewportHeight: 260,
        contentHeight: 100 * 26,
        stickyHeightAt: () => 0,
      }),
    ).toBe(100 * 26 - 260);
  });
});

describe("scrollTopToAlignRowCentered", () => {
  it("centers a row in the viewport when no sticky headers are present", () => {
    // index 50, rowTop = 1300, viewport = 260, row = 26.
    // targetOffset = (260 - 26) / 2 = 117.
    // scrollTop = 1300 - 117 = 1183.
    expect(
      scrollTopToAlignRowCentered({
        index: 50,
        rowHeight: 26,
        viewportHeight: 260,
        contentHeight: 2600,
        stickyHeightAt: () => 0,
      }),
    ).toBe(50 * 26 - (260 - 26) / 2);
  });

  it("accounts for sticky header height when centering", () => {
    // stickyHeight = 52, viewport = 260, row = 26.
    // available = 260 - 52 = 208.
    // targetOffset = 52 + (208 - 26) / 2 = 52 + 91 = 143.
    // scrollTop = 1300 - 143 = 1157.
    expect(
      scrollTopToAlignRowCentered({
        index: 50,
        rowHeight: 26,
        viewportHeight: 260,
        contentHeight: 2600,
        stickyHeightAt: () => 52,
      }),
    ).toBe(50 * 26 - 143);
  });

  it("clamps to 0 when row is near the top", () => {
    expect(
      scrollTopToAlignRowCentered({
        index: 1,
        rowHeight: 26,
        viewportHeight: 260,
        contentHeight: 2600,
        stickyHeightAt: () => 0,
      }),
    ).toBe(0);
  });

  it("clamps to maxScroll when row is near the bottom", () => {
    expect(
      scrollTopToAlignRowCentered({
        index: 99,
        rowHeight: 26,
        viewportHeight: 260,
        contentHeight: 100 * 26,
        stickyHeightAt: () => 0,
      }),
    ).toBe(100 * 26 - 260);
  });

  it("returns null for invalid row index or height", () => {
    expect(
      scrollTopToAlignRowCentered({
        index: -1,
        rowHeight: 26,
        viewportHeight: 260,
        contentHeight: 2600,
        stickyHeightAt: () => 0,
      }),
    ).toBeNull();
    expect(
      scrollTopToAlignRowCentered({
        index: 10,
        rowHeight: 0,
        viewportHeight: 260,
        contentHeight: 2600,
        stickyHeightAt: () => 0,
      }),
    ).toBeNull();
  });
});

describe("fixedVirtualWindow narrow buffers", () => {
  it("follows every move with a buffer below the guard and keeps an unchanged window's identity", () => {
    const opts = { count: 1_000, viewportHeight: 520, rowHeight: 26, bufferViewports: 0.25 };
    const first = fixedVirtualWindow({ ...opts, scrollTop: 2_600 });
    // 20 visible rows with 5 buffered on each side.
    expect(first).toEqual({ startIndex: 95, endIndex: 124 });
    const moved = fixedVirtualWindow({ ...opts, scrollTop: 2_600 + 26 * 3, previous: first ?? undefined });
    expect(moved).toEqual({ startIndex: 98, endIndex: 127 });
    expect(fixedVirtualWindow({ ...opts, scrollTop: 2_600 + 26 * 3, previous: moved ?? undefined })).toBe(moved);
    // A wider buffer never reuses a narrow window.
    expect(fixedVirtualWindow({ ...opts, bufferViewports: 1.5, scrollTop: 2_600 + 26 * 3, previous: moved ?? undefined }))
      .toEqual({ startIndex: 73, endIndex: 152 });
  });
});
