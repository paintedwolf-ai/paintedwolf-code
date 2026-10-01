// @vitest-environment jsdom
import { stubClient } from "../../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import {
  ensureTranscriptRevealAnchorLoaded,
  watchTranscriptLoadMore,
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

describe("watchTranscriptLoadMore", () => {
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
    const stop = watchTranscriptLoadMore(el, {
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
      stop();
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

    const stop = watchTranscriptLoadMore(el, {
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
      stop();
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

    const stop = watchTranscriptLoadMore(el, {
      client: () => client,
      appStore,
      sessionId: () => "s-prefetch",
    });
    try {
      await vi.waitFor(() => {
        expect(listSessionMessages).toHaveBeenCalledTimes(1);
      });
    } finally {
      stop();
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

    const stop = watchTranscriptLoadMore(el, {
      client: () => client,
      appStore,
      sessionId: () => "s-far",
    });
    try {
      el.dispatchEvent(new Event("scroll"));
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      expect(listSessionMessages).not.toHaveBeenCalled();
    } finally {
      stop();
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

    const stop = watchTranscriptLoadMore(el, {
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
      stop();
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

    const stop = watchTranscriptLoadMore(el, {
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
      stop();
      el.remove();
    }
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
});
