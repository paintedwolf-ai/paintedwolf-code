// @vitest-environment jsdom
import { createRoot } from "solid-js";
import { stubClient } from "../../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import {
  createTranscriptHistoryView,
  ensureTranscriptRevealAnchorLoaded,
  loadTranscriptPage,
  createTranscriptPaging,
} from "./transcript-load-more.ts";
import {
  clearTranscriptEntryMemory,
  hasTranscriptEntryMemory,
} from "../presentation/transcript-entry.ts";
import { createAppStore } from "../../../store/app-state.ts";
import type { Message } from "../../../api/types.ts";

function older(i: number): Message {
  return {
    id: `old-${i}`,
    role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
    content: `older ${i}`,
    created_at: "2026-01-01T00:00:00Z",
    ord: i + 1,
  };
}

/** Stream host with controlled scroll geometry. */
function mockStreamHost(opts: {
  scrollTop: number;
  scrollHeight: () => number;
  clientHeight?: number;
}) {
  const el = document.createElement("div");
  el.className = "den-chat-stream";
  Object.defineProperty(el, "clientHeight", {
    value: opts.clientHeight ?? 0,
    configurable: true,
  });
  let scrollTop = opts.scrollTop;
  const writes: number[] = [];
  Object.defineProperty(el, "scrollTop", {
    get: () => scrollTop,
    set: (next: number) => {
      writes.push(next);
      scrollTop = next;
    },
    configurable: true,
  });
  Object.defineProperty(el, "scrollHeight", {
    get: opts.scrollHeight,
    configurable: true,
  });
  document.body.appendChild(el);
  return { el, writes };
}

describe("createTranscriptPaging", () => {
  it("reads from the end of gapped history back to the live tail", async () => {
    const row = (ord: number): Message => ({ ...older(ord - 1), seq: ord });
    const rows = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => row(from + i));
    const page = (messages: Message[], before: boolean, after: boolean) => ({
      messages,
      ...(before ? { before_cursor: "older" } : {}),
      ...(after ? { after_cursor: "newer" } : {}),
      watermark: 800, turn_clocks: {}, turn_loads: {},
    });
    const ordOf = (id?: string) => (id ? Number(id.slice("old-".length)) + 1 : 0);
    const appStore = createAppStore();
    appStore.actions.installTranscriptBaseline("s-gap", rows(701, 800), 800, { hasMoreBefore: true, hasMoreAfter: false });
    for (let from = 601; from >= 1; from -= 100) {
      appStore.actions.loadTranscriptPage({ edge: "older", beforeMessageId: row(from + 100).id }, page(rows(from, from + 99), from > 1, true));
    }
    expect(appStore.state.transcript.hasTailGap).toBe(true);
    const listSessionMessages = vi.fn(async (_id: string, opts?: { afterMessageId?: string }) => {
      const after = ordOf(opts?.afterMessageId);
      const messages = rows(after + 1, Math.min(after + 100, 800));
      return page(messages, true, (messages.at(-1)?.ord ?? 800) < 800);
    });
    // The reader sits at the bottom of presented history, far from its top.
    const { el } = mockStreamHost({ scrollTop: 5_000, scrollHeight: () => 5_500, clientHeight: 500 });
    const stop = createTranscriptPaging(el, {
      client: () => stubClient({ listSessionMessages }),
      appStore,
      sessionId: () => "s-gap",
    });
    try {
      await vi.waitFor(() => expect(appStore.state.transcript.hasTailGap).toBe(false));
      expect(listSessionMessages.mock.calls.map(([, opts]) => ordOf(opts?.afterMessageId))).toEqual([500, 600, 700]);
      const ords = appStore.state.messages.map((message) => message.ord ?? 0);
      for (let i = 1; i < ords.length; i++) expect(ords[i]).toBe((ords[i - 1] ?? 0) + 1);
      expect(ords.at(-1)).toBe(800);
    } finally {
      stop.dispose();
      el.remove();
    }
  });

  it("leaves scroll anchoring across a prepend to the virtualizer", async () => {
    // The virtualizer controls prepend anchoring.
    const appStore = createAppStore();
    // Prepended rows grow the virtual scroll height.
    const { el, writes } = mockStreamHost({
      scrollTop: 0,
      scrollHeight: () => appStore.state.messages.length * 72,
    });
    appStore.actions.installTranscriptBaseline("s1", [older(50)], 51, {
      hasMoreBefore: true,
      hasMoreAfter: false,
    });
    const listSessionMessages = vi.fn().mockResolvedValue({
      messages: Array.from({ length: 20 }, (_, i) => older(i)),
      turn_clocks: {},
      turn_loads: {},
    });
    const client = stubClient({ listSessionMessages });

    const stop = createTranscriptPaging(el, {
      client: () => client,
      appStore,
      sessionId: () => "s1",
    });
    try {
      el.dispatchEvent(new Event("scroll"));
      await vi.waitFor(() => {
        expect(listSessionMessages).toHaveBeenCalledTimes(1);
      });
      await Promise.resolve();
      await Promise.resolve();
      expect(appStore.state.messages.length).toBeGreaterThan(1);
      expect(writes).toEqual([]);
    } finally {
      stop.dispose();
      el.remove();
    }
  });

  it("prefetches older history within two viewports of the loaded top", async () => {
    const appStore = createAppStore();
    const { el } = mockStreamHost({
      scrollTop: 780,
      clientHeight: 400,
      scrollHeight: () => 20_000,
    });
    appStore.actions.installTranscriptBaseline("s-prefetch", [older(50)], 51, {
      hasMoreBefore: true,
      hasMoreAfter: false,
    });
    const listSessionMessages = vi.fn().mockResolvedValue({
      messages: [older(40)],
      turn_clocks: {},
      turn_loads: {},
    });
    const client = stubClient({ listSessionMessages });

    const stop = createTranscriptPaging(el, {
      client: () => client,
      appStore,
      sessionId: () => "s-prefetch",
    });
    try {
      await vi.waitFor(() => {
        expect(listSessionMessages).toHaveBeenCalledTimes(1);
      });
    } finally {
      stop.dispose();
      el.remove();
    }
  });

  it("leaves older history for later beyond two viewports of the loaded top", async () => {
    const appStore = createAppStore();
    const { el } = mockStreamHost({
      scrollTop: 820,
      clientHeight: 400,
      scrollHeight: () => 20_000,
    });
    appStore.actions.installTranscriptBaseline("s-far", [older(50)], 51, {
      hasMoreBefore: true,
      hasMoreAfter: false,
    });
    const listSessionMessages = vi.fn();
    const client = stubClient({ listSessionMessages });

    const stop = createTranscriptPaging(el, {
      client: () => client,
      appStore,
      sessionId: () => "s-far",
    });
    try {
      el.dispatchEvent(new Event("scroll"));
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      expect(listSessionMessages).not.toHaveBeenCalled();
    } finally {
      stop.dispose();
      el.remove();
    }
  });

  it("loads on bind until an underfilled transcript exhausts older history", async () => {
    const appStore = createAppStore();
    const { el } = mockStreamHost({
      scrollTop: 0,
      scrollHeight: () => appStore.state.messages.length * 72,
    });
    appStore.actions.installTranscriptBaseline("s-fill", [older(50)], 51, {
      hasMoreBefore: true,
      hasMoreAfter: false,
    });
    const listSessionMessages = vi
      .fn()
      .mockResolvedValueOnce({
        messages: [older(40)],
        turn_clocks: {},
        turn_loads: {},
        before_cursor: "older",
      })
      .mockResolvedValueOnce({
        messages: [older(30)],
        turn_clocks: {},
        turn_loads: {},
      });
    const client = stubClient({ listSessionMessages });

    const stop = createTranscriptPaging(el, {
      client: () => client,
      appStore,
      sessionId: () => "s-fill",
    });
    try {
      await vi.waitFor(() => {
        expect(listSessionMessages).toHaveBeenCalledTimes(2);
      });
      expect(appStore.state.transcript.hasMoreBefore).toBe(false);
    } finally {
      stop.dispose();
      el.remove();
    }
  });

  it("does not spin when automatic backfill receives a stalled cursor", async () => {
    const appStore = createAppStore();
    const { el } = mockStreamHost({
      scrollTop: 0,
      scrollHeight: () => appStore.state.messages.length * 72,
    });
    appStore.actions.installTranscriptBaseline(
      "s-stalled-fill",
      [older(50)],
      51,
      {
        hasMoreBefore: true,
        hasMoreAfter: false,
      },
    );
    const listSessionMessages = vi.fn().mockResolvedValue({
      messages: [older(50)],
      turn_clocks: {},
      turn_loads: {},
      before_cursor: "older",
    });
    const client = stubClient({ listSessionMessages });

    const stop = createTranscriptPaging(el, {
      client: () => client,
      appStore,
      sessionId: () => "s-stalled-fill",
    });
    try {
      await vi.waitFor(() => {
        expect(listSessionMessages).toHaveBeenCalledTimes(1);
      });
      await new Promise<void>((resolve) =>
        requestAnimationFrame(() => resolve()),
      );
      expect(listSessionMessages).toHaveBeenCalledTimes(1);
    } finally {
      stop.dispose();
      el.remove();
    }
  });

  it("does not alternate page edges when compact history has both edges in reach", async () => {
    const rows = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => older(from + i - 1));
    const page = (from: number, to: number) => ({
      messages: rows(from, to), watermark: 1_000, before_cursor: from > 1 ? "older" : undefined,
      after_cursor: to < 1_000 ? "newer" : undefined, turn_clocks: {}, turn_loads: {},
    });
    const appStore = createAppStore();
    appStore.actions.installTranscriptBaseline("compact", rows(901, 1_000), 1_000, { hasMoreBefore: true });
    for (let from = 801; from >= 301; from -= 100) {
      appStore.actions.loadTranscriptPage({ edge: "older", beforeMessageId: older(from + 99).id }, page(from, from + 99));
    }
    expect(appStore.state.transcript.hasTailGap).toBe(true);
    const directions: string[] = [];
    const listSessionMessages = vi.fn(async (_id: string, options?: { beforeMessageId?: string; afterMessageId?: string }) => {
      const before = options?.beforeMessageId;
      const after = options?.afterMessageId;
      const boundary = Number((before ?? after ?? "").slice(4)) + 1;
      directions.push(before ? "older" : "newer");
      return before ? page(Math.max(1, boundary - 100), boundary - 1) : page(boundary + 1, Math.min(1_000, boundary + 100));
    });
    const { el } = mockStreamHost({ scrollTop: 500, scrollHeight: () => 2_000, clientHeight: 500 });
    const paging = createTranscriptPaging(el, { client: () => stubClient({ listSessionMessages }), appStore, sessionId: () => "compact" });
    try {
      await new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
      expect(directions).toEqual([]);
      paging.readerInput("up");
      await vi.waitFor(() => expect(appStore.state.transcript.hasMoreBefore).toBe(false));
      expect(directions).toEqual(["older", "older", "older"]);
      paging.readerInput("down");
      await vi.waitFor(() => expect(appStore.state.transcript.hasTailGap).toBe(false));
      expect(directions.slice(3).every(edge => edge === "newer")).toBe(true);
      expect(Object.keys(appStore.state.transcript.pages).length).toBeLessThanOrEqual(5);
      expect(appStore.state.transcript.tail.at(-1)?.ord).toBe(1_000);
    } finally { paging.dispose(); el.remove(); }
  });

  it("rejects a page after the session view generation changes", async () => {
    const appStore = createAppStore();
    appStore.actions.installTranscriptBaseline("s-generation", [older(50)], 51, { hasMoreBefore: true });
    let resolve!: (value: { messages: Message[]; turn_clocks: Record<string, never>; turn_loads: Record<string, never> }) => void;
    const response = new Promise<{ messages: Message[]; turn_clocks: Record<string, never>; turn_loads: Record<string, never> }>(done => { resolve = done; });
    const pending = loadTranscriptPage(stubClient({ listSessionMessages: () => response }), appStore, "s-generation", "older");
    appStore.actions.invalidateSessionViewRequests();
    resolve({ messages: [older(40)], turn_clocks: {}, turn_loads: {} });
    expect(await pending).toBe(false);
    expect(appStore.state.messages.map(row => row.id)).toEqual(["old-50"]);
  });

  it("retries a transient failure after renewed reader input", async () => {
    const appStore = createAppStore();
    appStore.actions.installTranscriptBaseline("s-retry", [older(50)], 51, { hasMoreBefore: true });
    const { el } = mockStreamHost({ scrollTop: 0, scrollHeight: () => 10_000, clientHeight: 500 });
    const listSessionMessages = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValue({
      messages: [older(40)], turn_clocks: {}, turn_loads: {},
    });
    const stop = createTranscriptPaging(el, {
      client: () => stubClient({ listSessionMessages }), appStore, sessionId: () => "s-retry",
    });
    try {
      await vi.waitFor(() => expect(listSessionMessages).toHaveBeenCalledTimes(1));
      stop.readerInput("up");
      await vi.waitFor(() => expect(listSessionMessages).toHaveBeenCalledTimes(2));
      expect(appStore.state.messages.map(row => row.id)).toEqual(["old-40", "old-50"]);
    } finally { stop.dispose(); el.remove(); }
  });

  it("retries after input received before an in-flight request fails", async () => {
    const appStore = createAppStore();
    appStore.actions.installTranscriptBaseline("s-flight", [older(50)], 51, { hasMoreBefore: true });
    const { el } = mockStreamHost({ scrollTop: 0, scrollHeight: () => 10_000, clientHeight: 500 });
    let reject!: (reason: Error) => void;
    const first = new Promise<never>((_resolve, fail) => { reject = fail; });
    const listSessionMessages = vi.fn().mockReturnValueOnce(first).mockResolvedValue({
      messages: [older(40)], turn_clocks: {}, turn_loads: {},
    });
    const paging = createTranscriptPaging(el, { client: () => stubClient({ listSessionMessages }), appStore, sessionId: () => "s-flight" });
    try {
      await vi.waitFor(() => expect(listSessionMessages).toHaveBeenCalledTimes(1));
      paging.readerInput("up");
      reject(new Error("offline"));
      await vi.waitFor(() => expect(listSessionMessages).toHaveBeenCalledTimes(2));
      expect(appStore.state.messages.map(row => row.id)).toEqual(["old-40", "old-50"]);
    } finally { paging.dispose(); el.remove(); }
  });

  it("walks through deep history until the anchor is resident", async () => {
    const appStore = createAppStore();
    appStore.actions.installTranscriptBaseline("s-deep", [older(100)], 101, {
      hasMoreBefore: true,
      hasMoreAfter: false,
    });
    let nextOrd = 99;
    const listSessionMessages = vi.fn().mockImplementation(async () => {
      const message = older(nextOrd);
      nextOrd -= 1;
      return {
        messages: [message],
        turn_clocks: {},
        turn_loads: {},
        before_cursor: nextOrd >= 60 ? "older" : undefined,
      };
    });
    const client = stubClient({ listSessionMessages });

    await expect(
      ensureTranscriptRevealAnchorLoaded(client, appStore, "s-deep", {
        chicklet: "message",
        anchorId: "old-65",
      }),
    ).resolves.toBe(true);
    expect(listSessionMessages.mock.calls.length).toBeGreaterThan(32);
  });

  it("stops a reveal walk when the server does not advance the before cursor", async () => {
    const appStore = createAppStore();
    appStore.actions.installTranscriptBaseline("s-stalled", [older(50)], 51, {
      hasMoreBefore: true,
      hasMoreAfter: false,
    });
    const listSessionMessages = vi.fn().mockResolvedValue({
      messages: [older(50)],
      turn_clocks: {},
      turn_loads: {},
      before_cursor: "older",
    });
    const client = stubClient({ listSessionMessages });

    await expect(
      ensureTranscriptRevealAnchorLoaded(client, appStore, "s-stalled", {
        chicklet: "message",
        anchorId: "missing",
      }),
    ).resolves.toBe(false);
    expect(listSessionMessages).toHaveBeenCalledTimes(1);
  });

  it("baselines an older page so scrolling back does not replay entry fades", () => {
    clearTranscriptEntryMemory("s-older");
    const appStore = createAppStore();
    appStore.actions.installTranscriptBaseline("s-older", [older(50)], 51, {
      hasMoreBefore: true,
      hasMoreAfter: false,
    });
    expect(hasTranscriptEntryMemory("s-older", "old-50")).toBe(true);
    expect(hasTranscriptEntryMemory("s-older", "old-3")).toBe(false);

    appStore.actions.loadTranscriptPage({ edge: "older", beforeMessageId: "old-50" }, {
      messages: Array.from({ length: 5 }, (_, i) => older(i)),
      watermark: 51,
      turn_clocks: {},
      turn_loads: {},
    });

    for (let i = 0; i < 5; i++) {
      expect(hasTranscriptEntryMemory("s-older", `old-${i}`)).toBe(true);
    }
    clearTranscriptEntryMemory("s-older");
  });

  it("seeks by ordinal when the scrollport lands inside an unloaded runway", async () => {
    const appStore = createAppStore();
    const prepareLanding = vi.fn();
    const { el } = mockStreamHost({
      scrollTop: 5_000,
      clientHeight: 500,
      scrollHeight: () => 50_000,
    });
    appStore.actions.installTranscriptBaseline(
      "s-seek",
      Array.from({ length: 100 }, (_, i) => older(400 + i)),
      500,
      {
        hasMoreBefore: true,
        hasMoreAfter: false,
        bounds: { firstOrd: 1, lastOrd: 500 },
      },
    );
    expect(appStore.state.transcript.bounds).toEqual({ firstOrd: 1, lastOrd: 500 });
    const listSessionMessages = vi.fn().mockResolvedValue({
      messages: Array.from({ length: 100 }, (_, i) => older(i)),
      turn_clocks: {},
      turn_loads: {},
      before_cursor: undefined,
      after_cursor: "newer",
      ord_bounds: { first_ord: 1, last_ord: 500 },
    });
    const client = stubClient({ listSessionMessages });

    const stop = createTranscriptPaging(el, {
      client: () => client,
      appStore,
      sessionId: () => "s-seek",
      geometry: () => ({
        residentStartPx: 40_000,
        residentEndPx: 50_000,
        residentSpan: { firstOrd: 401, lastOrd: 500 },
        unloaded: { before: 400, after: 0 },
        prepareLanding,
      }),
    });
    try {
      await vi.waitFor(() => {
        expect(listSessionMessages).toHaveBeenCalledTimes(1);
      });
      expect(prepareLanding).toHaveBeenCalledWith(5_000);
      expect(listSessionMessages).toHaveBeenCalledWith(
        "s-seek",
        expect.objectContaining({ afterOrd: 0 }),
      );
      expect(appStore.state.transcript.hasTailGap).toBe(true);
    } finally {
      stop.dispose();
      el.remove();
    }
  });

  it("protects older pages in the reader's reach during live appends and reclaims them after scrolling away", async () => {
    const appStore = createAppStore();
    const rows = (from: number, to: number) =>
      Array.from({ length: to - from + 1 }, (_, i) => older(from + i - 1));
    appStore.actions.installTranscriptBaseline("s-reach", rows(501, 700), 700, {
      hasMoreBefore: true,
      bounds: { firstOrd: 1, lastOrd: 700 },
    });
    for (let from = 401; from >= 1; from -= 100) {
      appStore.actions.loadTranscriptPage(
        { edge: "older", beforeMessageId: older(from + 99).id },
        {
          messages: rows(from, from + 99),
          ...(from > 1 ? { before_cursor: "older" } : {}),
          watermark: 700,
          turn_clocks: {},
          turn_loads: {},
        },
      );
    }
    let scrollTop = 0;
    const { el } = mockStreamHost({
      scrollTop: 0,
      clientHeight: 500,
      scrollHeight: () => 50_000,
    });
    Object.defineProperty(el, "scrollTop", {
      get: () => scrollTop,
      set: (v: number) => { scrollTop = v; },
      configurable: true,
    });
    const stop = createTranscriptPaging(el, {
      client: () => stubClient({ listSessionMessages: vi.fn() }),
      appStore,
      sessionId: () => "s-reach",
      geometry: () => ({
        residentStartPx: 0,
        residentEndPx: 50_000,
        residentSpan: { firstOrd: 1, lastOrd: 700 },
        unloaded: { before: 0, after: 0 },
        prepareLanding: () => {},
      }),
    });
    try {
      el.dispatchEvent(new Event("scroll"));
      await new Promise<void>((resolve) => queueMicrotask(resolve));
      appStore.actions.upsertMessage({
        ...older(700),
        id: "live-701",
        ord: 701,
        seq: 701,
      });
      expect(appStore.state.transcript.pages["1:100"]).toBeDefined();
      expect(Object.keys(appStore.state.transcript.pages).length).toBe(6);

      scrollTop = 45_000;
      el.dispatchEvent(new Event("scroll"));
      await vi.waitFor(() => {
        expect(appStore.state.transcript.pages["1:100"]).toBeUndefined();
      });
      expect(Object.keys(appStore.state.transcript.pages).length).toBe(5);
    } finally {
      stop.dispose();
      el.remove();
    }
  });

  it("derives history view geometry from either an app store or resident message items", () => {
    const appStore = createAppStore();
    const msgs = [older(10), older(19)];
    appStore.actions.installTranscriptBaseline("s-view", msgs, 20, {
      hasMoreBefore: true,
      bounds: { firstOrd: 1, lastOrd: 20 },
    });
    const items = msgs.map((m) => ({ kind: "user" as const, key: m.id, text: m.content }));
    const prepareLanding = vi.fn();
    const fakeVirtualizer = {
      residentExtent: () => ({ startPx: 1_000, endPx: 2_000 }),
      prepareLanding,
    } as unknown as Parameters<ReturnType<typeof createTranscriptHistoryView>["geometry"]>[0];

    createRoot((dispose) => {
      const withStore = createTranscriptHistoryView({
        enabled: () => true,
        messages: () => msgs,
        items: () => items,
        appStore: () => appStore,
      });
      expect(withStore.unloadedOrds()).toEqual({ before: 10, after: 0 });
      expect(withStore.residentRowSpan()).toEqual({ count: 2, ordSpan: 10 });
      const geo = withStore.geometry(fakeVirtualizer);
      expect(geo).toMatchObject({
        residentStartPx: 1_000,
        residentEndPx: 2_000,
        residentSpan: { firstOrd: 11, lastOrd: 20 },
        unloaded: { before: 10, after: 0 },
      });
      geo?.prepareLanding(300);
      expect(prepareLanding).toHaveBeenCalledWith(300);

      const withoutStore = createTranscriptHistoryView({
        enabled: () => false,
        messages: () => msgs,
        items: () => items,
        appStore: () => undefined,
      });
      expect(withoutStore.unloadedOrds()).toEqual({ before: 0, after: 0 });
      expect(withoutStore.residentRowSpan()).toEqual({ count: 2, ordSpan: 10 });
      expect(withoutStore.geometry(fakeVirtualizer)).toBeNull();
      dispose();
    });
  });
});

