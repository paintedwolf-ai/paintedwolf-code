import { DEFAULT_TRANSCRIPT_SPACING, setTranscriptSpacing, transcriptGeometryKey, transcriptInteriorSignature, transcriptSeamPx, type TranscriptSeam } from "../../chat/transcript/layout/transcript-spacing.ts";
import { fontSelection } from "../../settings/appearance/font-prefs.ts";
import { effectiveTextScale } from "../../platform/desktop/accessibility-text-size.ts";
import { transcriptLayoutRevision } from "../../chat/transcript/layout/transcript-layout-revision.ts";
import { resetSessionTranscriptChatTest } from "./session-transcript-chat-test-harness.ts";

import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { render, waitFor } from "@solidjs/testing-library";
import { SessionTranscript } from "./transcript-viewport-test-harness.tsx";
import { transcriptScrollHostElement } from "./SessionTranscript.tsx";
import type { RawTranscriptItem } from "../../chat/transcript/projection/transcript-item-model.ts";
import type { Message } from "../../api/types.ts";
import {
  clearTranscriptEntryMemory,
  hasTranscriptEntryMemory,
  noteTranscriptEntryBaseline,
} from "../../chat/transcript/presentation/transcript-entry.ts";
import { TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT } from "../../chat/transcript/presentation/transcript-disclosure.tsx";
import { requireScrollportMotionForViewport } from "../../platform/scrolling/scrollport-motion.ts";
import {
  clearTranscriptRowHeightsForTests,
  seedTranscriptRowHeightsForTests as seedHeights,
  transcriptRowHeightForScope as readHeight,
} from "../../chat/transcript/layout/transcript-row-heights-persist.ts";

const geometry = vi.hoisted(() => ({ width: 0 }));
vi.mock("../../layout/shared-resize-observer.ts", async (original) => {
  const actual = await original<typeof import("../../layout/shared-resize-observer.ts")>();
  return { ...actual, observeSharedContentBox: (element: Element, listener: import("../../layout/shared-resize-observer.ts").SharedResizeListener) => {
    if (geometry.width > 0 && element.classList.contains("den-chat-stream-inner")) listener({ width: geometry.width, height: 900 });
    return actual.observeSharedContentBox(element, listener);
  } };
});
function heightKey(presentation: string): string {
  return `${transcriptGeometryKey({ widthPx: geometry.width, remPx: 16, bodyPx: 16 }, transcriptInteriorSignature(DEFAULT_TRANSCRIPT_SPACING), {
    uiFont: fontSelection("ui"), monoFont: fontSelection("mono"), scale: effectiveTextScale(), revision: transcriptLayoutRevision(),
  })}:${presentation}`;
}
function seedTranscriptRowHeightsForTests(scope: Parameters<typeof seedHeights>[0], rows: Parameters<typeof seedHeights>[1]): void {
  seedHeights(scope, Object.fromEntries(Object.entries(rows).map(([key, presentations]) => [key,
    Object.fromEntries(Object.entries(presentations).map(([presentation, value]) => [heightKey(presentation), value]))])));
}
function transcriptRowHeightForScope(scope: Parameters<typeof readHeight>[0], key: string, presentation: string): number | undefined {
  return readHeight(scope, key, heightKey(presentation));
}

function mockTranscriptRowHeight(
  readHeight: (row: HTMLElement) => number,
): () => void {
  geometry.width = 700;
  const descriptor = Object.getOwnPropertyDescriptor(
    HTMLElement.prototype,
    "offsetHeight",
  );
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return this.classList.contains("transcript-viewport-row")
        ? readHeight(this)
        : 0;
    },
  });
  return () => {
    geometry.width = 0;
    if (descriptor) {
      Object.defineProperty(HTMLElement.prototype, "offsetHeight", descriptor);
    } else {
      Reflect.deleteProperty(HTMLElement.prototype, "offsetHeight");
    }
  };
}

describe("SessionTranscript virtual window", () => {
  afterEach(() => { geometry.width = 0; return resetSessionTranscriptChatTest(); });

  it("waits for a detached transcript root to enter its chat scroll host", () => {
    const root = document.createElement("section");
    expect(transcriptScrollHostElement(root)).toBeNull();

    document.body.appendChild(root);
    expect(() => transcriptScrollHostElement(root)).toThrow(
      "Chat transcript requires a scroll host.",
    );

    const host = document.createElement("div");
    host.className = "den-chat-stream";
    host.appendChild(root);
    document.body.appendChild(host);
    expect(transcriptScrollHostElement(root)).toBe(host);
    host.remove();
  });

  it("attaches the scroll host when the first row enters an empty chat", async () => {
    const [messages, setMessages] = createSignal<Message[]>([]);
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s-first-row"
        messages={messages()}
      />
    ));

    expect(container.querySelector("[data-testid=message-stream]")).toBeNull();

    setMessages([
      {
        id: "first-row",
        role: "user",
        origin: "user",
        authority: "user",
        trust_tier: "trusted",
        content: "First prompt",
        created_at: "2026-01-01T00:00:00Z",
        ord: 1,
      },
    ]);
    await Promise.resolve();

    expect(
      container.querySelector('[data-msg-id="first-row"]'),
    ).toBeTruthy();
  });

  it("keeps every mounted row bound to its own item through a same-length substitution", async () => {
    // User rows render their content synchronously (no prose reveal).
    const mk = (id: string, i: number): Message => ({
      id,
      role: "user",
      origin: "user",
      authority: "user",
      trust_tier: "trusted",
      content: `body of ${id}`,
      created_at: `2026-01-01T00:00:${String(i).padStart(2, "0")}Z`,
      ord: i + 1,
    });
    const [messages, setMessages] = createSignal<Message[]>(
      Array.from({ length: 30 }, (_, i) => mk(`m-${i}`, i)),
    );
    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-swap" messages={messages()} />
    ));

    // Replace a row without changing the list length.
    setMessages((prev) => prev.map((m, i) => (i === 10 ? mk("m-swapped", 10) : m)));
    await Promise.resolve();

    const rows = [
      ...container.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-msg-id]"),
    ];
    expect(rows.length).toBeGreaterThan(0);
    const ids = rows.map((row) => row.dataset.msgId);
    // A stale index resolution renders a neighbouring item under two row keys.
    expect(new Set(ids).size).toBe(ids.length);
    for (const row of rows) {
      const id = row.dataset.msgId ?? "";
      expect(row.textContent, `row ${id} renders another item's content`).toContain(
        `body of ${id}`,
      );
    }
  });

  it("bounds mounted chat rows to the virtual window for a large transcript", () => {
    const messages: Message[] = Array.from({ length: 200 }, (_, i) => ({
      id: `m-${i}`,
      role: i % 2 === 0 ? "user" : "assistant",
      origin: i % 2 === 0 ? "user" : "model",
      authority: i % 2 === 0 ? "user" : "none",
      trust_tier: "trusted",
      content: `row ${i}`,
      created_at: `2026-01-01T00:00:${String(i).padStart(2, "0")}Z`,
      ord: i + 1,
    }));
    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-virt" messages={messages} />
    ));
    const mounted = container.querySelectorAll(".transcript-viewport-row");
    expect(mounted.length).toBeGreaterThan(0);
    expect(mounted.length).toBeLessThanOrEqual(40);
    expect(mounted.length).toBeLessThan(messages.length);
    expect(container.querySelector("[data-deferred]")).toBeNull();
  });

  it("reconciles extent changes without measuring every virtual window move", async () => {
    const message = (index: number): Message => ({
      id: `extent-${index}`, role: "user", origin: "user", authority: "user",
      trust_tier: "trusted", content: `row ${index}`,
      created_at: new Date(Date.UTC(2026, 0, 1, 0, 0, index)).toISOString(), ord: index + 1,
    });
    const [messages, setMessages] = createSignal(Array.from({ length: 200 }, (_, index) => message(index)));
    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-extent" messages={messages()} />
    ));
    await new Promise(requestAnimationFrame);
    const host = container.querySelector<HTMLElement>(".den-chat-stream")!;
    const motion = requireScrollportMotionForViewport(host);
    const reconcile = vi.spyOn(motion, "scheduleLayoutReconcile");
    const mountedKeys = () => [...container.querySelectorAll<HTMLElement>(".transcript-viewport-row")]
      .map((row) => row.dataset.msgId).join(",");
    const before = mountedKeys();
    host.scrollTop = 5_000;
    host.dispatchEvent(new Event("scroll"));
    await new Promise(requestAnimationFrame);
    expect(mountedKeys()).not.toBe(before);
    expect(reconcile).not.toHaveBeenCalled();
    setMessages((previous) => [...previous, message(200)]);
    await Promise.resolve();
    expect(reconcile).toHaveBeenCalledOnce();
    reconcile.mockRestore();
  });

  it("renders the virtual window in normal flow when layout is unavailable", async () => {
    clearTranscriptRowHeightsForTests();
    const messages: Message[] = Array.from({ length: 6 }, (_, i) => ({
      id: `stack-${i}`,
      role: i % 2 === 0 ? "user" : "assistant",
      origin: i % 2 === 0 ? "user" : "model",
      authority: i % 2 === 0 ? "user" : "none",
      trust_tier: "trusted",
      content: `row ${i}`,
      created_at: `2026-01-01T00:00:${String(i).padStart(2, "0")}Z`,
      ord: i + 1,
    }));
    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-stack" messages={messages} />
    ));
    await Promise.resolve();

    // A day label leads the rows and turn tails close them; every row stacks in flow.
    const rows = [...container.querySelectorAll<HTMLElement>(".transcript-viewport-row")];
    const offsets = rows.map((row) => Number(row.dataset.virtualStart));
    expect(offsets.length).toBeGreaterThan(1);
    // Two rows of one kind differ only by the seam each carries.
    const boxOf = (index: number) =>
      offsets[index + 1]! - offsets[index]! -
      transcriptSeamPx(rows[index]!.dataset.seam as TranscriptSeam | undefined);
    const prose = rows.flatMap((row, index) =>
      index + 1 < rows.length && row.dataset.msgId?.startsWith("stack-") ? [index] : [],
    );
    expect(prose.length).toBeGreaterThan(1);
    for (const index of prose) expect(boxOf(index), rows[index]!.dataset.msgId).toBeCloseTo(boxOf(prose[0]!));
    // Alternating prompts and answers exercise more than one rung.
    expect(new Set(rows.map((row) => row.dataset.seam)).size).toBeGreaterThan(1);
    const stream = container.querySelector<HTMLElement>(
      ".den-chat-stream-inner",
    );
    expect(stream?.style.height).toBe("");
    // The virtual total lives in the runways and the mounted rows.
    expect(stream?.style.minHeight).toBe("");
    expect(
      [
        ...container.querySelectorAll<HTMLElement>("[data-transcript-runway]"),
      ].map((runway) => runway.style.height),
    ).toEqual(["0px", "0px"]);
    expect(
      [...container.querySelectorAll<HTMLElement>(".transcript-viewport-row")]
        .every((row) => row.style.transform === ""),
    ).toBe(true);
    expect(container.querySelectorAll("[data-transcript-runway]")).toHaveLength(
      2,
    );
  });

  it("places reloaded visual rows from a matching geometry generation", () => {
    geometry.width = 700;
    clearTranscriptRowHeightsForTests();
    const scope = { projectId: "p-reload", sessionId: "s-reload" };
    seedTranscriptRowHeightsForTests(scope, {
      visual: { collapsed: 353 },
      summary: { collapsed: 240 },
    });
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        projectId={scope.projectId}
        sessionId={scope.sessionId}
        messages={[
          { id: "visual", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "visual", created_at: "t", ord: 1 },
          { id: "summary", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "summary", created_at: "t", ord: 2 },
        ]}
      />
    ));
    const rows = [
      ...container.querySelectorAll<HTMLElement>(".transcript-viewport-row"),
    ];
    const summary = rows.find((row) => row.dataset.msgId === "summary");
    expect(summary).toBeTruthy();
    // The first row carries no seam, so the next row starts at its measured height.
    expect(summary!.dataset.virtualStart).toBe(String(353));
    clearTranscriptRowHeightsForTests();
  });

  it("keeps catalog-span seams inside the measured virtual extent", async () => {
    clearTranscriptRowHeightsForTests();
    const restoreRowHeight = mockTranscriptRowHeight((row) =>
      100 + transcriptSeamPx(row.dataset.seam as TranscriptSeam | undefined),
    );
    const scope = { projectId: "p-span-seam", sessionId: "s-span-seam" };
    try {
      const { container } = render(() => (
        <SessionTranscript
          layout="chat"
          projectId={scope.projectId}
          sessionId={scope.sessionId}
          messages={[]}
          transcriptSpans={[
            {
              kind: "run",
              runId: "",
              ambientSpan: true,
              items: [{ kind: "assistant", key: "ambient", text: "ambient" }],
            },
            {
              kind: "run",
              runId: "catalog",
              items: [{ kind: "assistant", key: "catalog", text: "catalog" }],
            },
            {
              kind: "run",
              runId: "after",
              items: [{ kind: "assistant", key: "after", text: "after" }],
            },
          ]}
        />
      ));
      await Promise.resolve();
      await Promise.resolve();
      await new Promise(requestAnimationFrame);

      const catalog = container.querySelector<HTMLElement>(
        '[data-msg-id="catalog"]',
      );
      // Entering a run and leaving it are both section seams, never a sum of two.
      expect(catalog?.dataset.seam).toBe("section");
      expect(
        container.querySelector<HTMLElement>('[data-msg-id="after"]')?.dataset.seam,
      ).toBe("section");
      expect(
        transcriptRowHeightForScope(scope, "catalog", "collapsed"),
      ).toBe(100);
      // The next row starts past the seam, so the seam is inside the extent.
      expect(
        container.querySelector<HTMLElement>('[data-msg-id="after"]')?.dataset
          .virtualStart,
      ).toBe(String(200 + transcriptSeamPx("section")));

      for (const sectionGap of [1.9375, 0, DEFAULT_TRANSCRIPT_SPACING.sectionGap]) {
        setTranscriptSpacing({ ...DEFAULT_TRANSCRIPT_SPACING, sectionGap });
        await Promise.resolve();
        expect(container.querySelector<HTMLElement>('[data-msg-id="after"]')?.dataset.virtualStart)
          .toBe(String(200 + transcriptSeamPx("section")));
        expect(transcriptRowHeightForScope(scope, "catalog", "collapsed")).toBe(100);
      }
    } finally {
      setTranscriptSpacing(DEFAULT_TRANSCRIPT_SPACING);
      restoreRowHeight();
      clearTranscriptRowHeightsForTests();
    }
  });

  it("measures every item that enters the virtual window", async () => {
    clearTranscriptRowHeightsForTests();
    const row = (i: number): Message => ({
      id: `m-${i}`,
      role: i % 2 === 0 ? "user" : "assistant",
      origin: i % 2 === 0 ? "user" : "model",
      authority: i % 2 === 0 ? "user" : "none",
      trust_tier: "trusted",
      content: `row ${i}`,
      created_at: "2026-01-01T00:00:00Z",
      ord: i + 1,
    });
    const restoreRowHeight = mockTranscriptRowHeight(() => 200);
    try {
      const tail = Array.from({ length: 40 }, (_, i) => row(i + 100));
      const [messages, setMessages] = createSignal<Message[]>(tail);
      const { container } = render(() => (
        <SessionTranscript
          layout="chat"
          sessionId="s-window"
          projectId="p-window"
          messages={messages()}
        />
      ));
      await Promise.resolve();
      await Promise.resolve();

      const firstMountedIds = [
        ...container.querySelectorAll<HTMLElement>(
          ".transcript-viewport-row[data-msg-id]",
        ),
      ].map((el) => el.getAttribute("data-msg-id"));
      expect(firstMountedIds).toContain("m-100");

      setMessages(Array.from({ length: 40 }, (_, i) => row(i)));
      await Promise.resolve();
      await Promise.resolve();
      await new Promise(requestAnimationFrame);

      const mounted = [
        ...container.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-msg-id]"),
      ];
      expect(
        mounted.some((el) =>
          firstMountedIds.includes(el.getAttribute("data-msg-id")),
        ),
      ).toBe(false);
      const unmeasured = mounted.filter(
        (el) =>
          transcriptRowHeightForScope(
            { projectId: "p-window", sessionId: "s-window" },
            el.getAttribute("data-msg-id") ?? "",
            "collapsed",
          ) === undefined,
      );
      expect(unmeasured).toHaveLength(0);

      // Turn tails sit between the message rows, so the stack is read whole.
      const stacked = [...container.querySelectorAll<HTMLElement>(".transcript-viewport-row")];
      const offsets = stacked.map((el) => Number(el.dataset.virtualStart));
      for (let i = 1; i < offsets.length; i++) {
        // A row's measured box carries its own seam, so offsets advance by that box.
        expect(offsets[i]! - offsets[i - 1]!, stacked[i - 1]!.dataset.msgId ?? stacked[i - 1]!.dataset.timeRow).toBe(200);
      }
    } finally {
      restoreRowHeight();
      clearTranscriptRowHeightsForTests();
    }
  });

  it("keeps published offsets consistent as mixed-height turns append and settle", async () => {
    clearTranscriptRowHeightsForTests();
    const scope = { projectId: "p-growing-turns", sessionId: "s-growing-turns" };
    // Turn tails are rows too; key their height the same way.
    const idOf = (row: HTMLElement) => row.dataset.msgId ?? row.dataset.timeRow ?? "";
    const rowHeight = (key: string) =>
      key.startsWith("progress-") ? 96 : key.startsWith("turn-tail:") ? 24 : 180;
    const restoreRowHeight = mockTranscriptRowHeight((row) => rowHeight(idOf(row)));
    const makeTurn = (turn: number): RawTranscriptItem[] => [
      { kind: "user", key: `user-${turn}`, text: `Request ${turn}` },
      { kind: "progress_complete", key: `progress-${turn}`, steps: [] },
      { kind: "assistant", key: `assistant-${turn}`, text: `Completed request ${turn}` },
    ];
    seedTranscriptRowHeightsForTests(scope, Object.fromEntries(
      Array.from({ length: 12 }, (_, turn) => [`progress-${turn}`, { collapsed: 2800 }]),
    ));
    const [items, setItems] = createSignal<RawTranscriptItem[]>([]);
    try {
      const { container } = render(() => (
        <SessionTranscript layout="chat" projectId={scope.projectId} sessionId={scope.sessionId}
          messages={[]} transcriptItems={items()} />
      ));
      for (let turn = 0; turn < 12; turn += 1) {
        setItems((rows) => [...rows, ...makeTurn(turn)]);
        await waitFor(() => {
          const mounted = [...container.querySelectorAll<HTMLElement>(".transcript-viewport-row")];
          expect(mounted.length).toBeGreaterThan(0);
          for (let index = 1; index < mounted.length; index += 1) {
            const previous = mounted[index - 1]!;
            const current = mounted[index]!;
            expect(Number(current.dataset.virtualStart) - Number(previous.dataset.virtualStart),
              `turn ${turn}: ${idOf(previous)} → ${idOf(current)}`).toBeCloseTo(rowHeight(idOf(previous)));
          }
        });
      }
    } finally {
      restoreRowHeight();
      clearTranscriptRowHeightsForTests();
    }
  });

  it("renders a pending checkpoint as an open marker, not a card", async () => {
    const items: RawTranscriptItem[] = [
      {
        kind: "checkpoint",
        key: "checkpoint:pending-1",
        parentMessageId: "assistant-1",
        meta: {
          checkpoint_id: "pending-1",
          kind: "tool_approval",
          status: "pending",
          tool: "capture_page",
          subject: "/index.html",
        },
      },
    ];
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        projectId="p-open-marker"
        sessionId="s-open-marker"
        transcriptItems={items}
        messages={[
          {
            id: "assistant-1",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content: "",
            created_at: "2026-01-01T00:00:00Z",
            ord: 1,
          },
        ]}
      />
    ));
    await Promise.resolve();

    const marker = container.querySelector(
      '[data-testid="checkpoint-open-marker"]',
    );
    expect(marker).toBeTruthy();
    expect(marker!.getAttribute("data-checkpoint-id")).toBe("pending-1");
    expect(
      container.querySelector('[data-testid="tool-approval-card"]'),
    ).toBeNull();
  });

  it("repositions following rows while a decision chicklet expands", async () => {
    clearTranscriptRowHeightsForTests();
    const items: RawTranscriptItem[] = [
      {
        kind: "checkpoint",
        key: "checkpoint:allow-1",
        parentMessageId: "assistant-1",
        meta: {
          checkpoint_id: "allow-1",
          kind: "tool_approval",
          status: "approved",
          tool: "capture_page",
          subject: "/index.html",
          grant_scope: "chat",
          grant_title: "Allow capture_page for this chat",
        },
      },
      { kind: "user", key: "after-allow", text: "following transcript row" },
    ];
    let expanded = false;
    // A real measurement includes the row's seam; the persisted height excludes it.
    const restoreRowHeight = mockTranscriptRowHeight((row) =>
      (row.dataset.msgId?.startsWith("checkpoint:") && expanded ? 180 : 32) +
      transcriptSeamPx(row.dataset.seam as TranscriptSeam | undefined),
    );
    try {
      const { container } = render(() => (
        <SessionTranscript
          layout="chat"
          projectId="p-chicklet-layout"
          sessionId="s-chicklet-layout"
          transcriptItems={items}
          messages={[
            {
              id: "assistant-1",
              role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
              content: "",
              created_at: "2026-01-01T00:00:00Z",
              ord: 1,
            },
            {
              id: "after-allow",
              role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
              content: "following transcript row",
              created_at: "2026-01-01T00:00:01Z",
              ord: 2,
            },
          ]}
        />
      ));
      await Promise.resolve();
      await Promise.resolve();

      const following = container.querySelector<HTMLElement>(
        '[data-msg-id="after-allow"]',
      );
      const chicklet = container.querySelector<HTMLDetailsElement>(
        '[data-testid="checkpoint-decision-chicklet"]',
      );
      expect(following).toBeTruthy();
      expect(chicklet).toBeTruthy();
      await waitFor(() => expect(transcriptRowHeightForScope(
        { projectId: "p-chicklet-layout", sessionId: "s-chicklet-layout" },
        "checkpoint:allow-1", "collapsed",
      )).toBe(32));
      const before = Number(following!.dataset.virtualStart);

      expanded = true;
      chicklet!.dispatchEvent(
        new Event(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, { bubbles: true }),
      );
      await Promise.resolve();

      await waitFor(() => expect(
        transcriptRowHeightForScope(
          { projectId: "p-chicklet-layout", sessionId: "s-chicklet-layout" },
          "checkpoint:allow-1",
          "collapsed",
        ),
      ).toBe(180));

      const after = Number(following!.dataset.virtualStart);
      expect(after - before).toBe(148);
    } finally {
      restoreRowHeight();
      clearTranscriptRowHeightsForTests();
    }
  });

  it("remeasures live row mutations when ResizeObserver delivery is absent", async () => {
    clearTranscriptRowHeightsForTests();
    let grown = false;
    const restoreRowHeight = mockTranscriptRowHeight((row) =>
      row.dataset.msgId === "growing" && grown ? 300 : 72,
    );
    try {
      const [text, setText] = createSignal("short response");
      const transcriptItems = () => [
        { kind: "assistant" as const, key: "growing", text: text() },
        { kind: "user" as const, key: "following", text: "next prompt" },
      ];
      const { container } = render(() => (
        <SessionTranscript
          layout="chat"
          projectId="p-live-growth"
          sessionId="s-live-growth"
          transcriptItems={transcriptItems()}
          messages={[]}
        />
      ));
      await Promise.resolve();
      await Promise.resolve();
      const following = container.querySelector<HTMLElement>(
        '[data-msg-id="following"]',
      );
      expect(following).toBeTruthy();
      await waitFor(() => expect(transcriptRowHeightForScope(
        { projectId: "p-live-growth", sessionId: "s-live-growth" },
        "growing", "collapsed",
      )).toBe(72));
      const before = Number(following!.dataset.virtualStart);

      grown = true;
      setText("a response that grew after its first row measurement");
      await Promise.resolve();
      await new Promise((resolve) => setTimeout(resolve, 30));
      await Promise.resolve();

      await waitFor(() => expect(Number(following!.dataset.virtualStart) - before).toBe(228));
      expect(following!.style.transform).toBe("");
    } finally {
      restoreRowHeight();
      clearTranscriptRowHeightsForTests();
    }
  });

  it("enters every row that enters the virtual window into the entry-fade ledger", async () => {
    clearTranscriptEntryMemory("s-fade");
    const row = (i: number): Message => ({
      id: `u-${i}`,
      role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
      content: `prompt ${i}`,
      created_at: "2026-01-01T00:00:00Z",
      ord: i + 1,
    });
    const tail = Array.from({ length: 30 }, (_, i) => row(i + 100));
    const [messages, setMessages] = createSignal<Message[]>(tail);
    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-fade" messages={messages()} />
    ));
    await Promise.resolve();
    const firstMounted = [
      ...container.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-msg-id]"),
    ].map((el) => el.getAttribute("data-msg-id") ?? "");
    expect(firstMounted.length).toBeGreaterThan(0);
    expect(
      firstMounted.every((key) => hasTranscriptEntryMemory("s-fade", key)),
    ).toBe(true);

    setMessages(Array.from({ length: 30 }, (_, i) => row(i)));
    await Promise.resolve();
    await Promise.resolve();

    const mounted = [
      ...container.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-msg-id]"),
    ];
    const forgotten = mounted
      .map((el) => el.getAttribute("data-msg-id") ?? "")
      .filter((key) => !hasTranscriptEntryMemory("s-fade", key));
    expect(forgotten).toEqual([]);
    clearTranscriptEntryMemory("s-fade");
  });

  it("slides the mounted window when the scroll host moves, even if the window length stays the same", async () => {
    const messages: Message[] = Array.from({ length: 80 }, (_, i) => ({
      id: `scroll-${i}`,
      role: i % 2 === 0 ? "user" : "assistant",
      origin: i % 2 === 0 ? "user" : "model",
      authority: i % 2 === 0 ? "user" : "none",
      trust_tier: "trusted",
      content: `row ${i}`,
      created_at: "2026-01-01T00:00:00Z",
      ord: i + 1,
    }));
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s-scroll-window"
        messages={messages}
      />
    ));
    const host = container.querySelector<HTMLElement>(".den-chat-stream");
    expect(host).toBeTruthy();
    Object.defineProperty(host!, "clientHeight", {
      value: 400,
      configurable: true,
    });
    await Promise.resolve();
    await Promise.resolve();
    await new Promise((resolve) => requestAnimationFrame(() => resolve(undefined)));
    Object.defineProperty(host!, "scrollHeight", {
      configurable: true,
      value: 16_000,
    });
    const maxTop = Math.max(0, host!.scrollHeight - host!.clientHeight);
    host!.scrollTop = maxTop;
    host!.dispatchEvent(new Event("scroll"));
    await new Promise(requestAnimationFrame);
    await waitFor(() => {
      const tailIds = [
        ...container.querySelectorAll<HTMLElement>(".transcript-viewport-row"),
      ].map((el) => el.getAttribute("data-msg-id"));
      expect(tailIds.some((id) => id === "scroll-79")).toBe(true);
      expect(tailIds.some((id) => id === "scroll-0")).toBe(false);
    });

    host!.scrollTop = 0;
    host!.dispatchEvent(new Event("scroll"));
    await new Promise(requestAnimationFrame);

    await waitFor(() => {
      const headIds = [
        ...container.querySelectorAll<HTMLElement>(".transcript-viewport-row"),
      ].map((el) => el.getAttribute("data-msg-id"));
      expect(headIds.some((id) => id === "scroll-0")).toBe(true);
      expect(headIds.some((id) => id === "scroll-79")).toBe(false);
    });
  });

  it("replaces a stale persisted hint once and yields to the event loop", async () => {
    clearTranscriptRowHeightsForTests();
    const scope = { projectId: "p-shrink", sessionId: "s-shrink" };
    seedTranscriptRowHeightsForTests(scope, {
      summary: { collapsed: 800 },
    });
    let heightReads = 0;
    const restoreRowHeight = mockTranscriptRowHeight(() => {
      heightReads += 1;
      return 40;
    });
    try {
      const { container } = render(() => (
        <SessionTranscript
          layout="chat"
          projectId={scope.projectId}
          sessionId={scope.sessionId}
          messages={[
            {
              id: "summary",
              role: "assistant",
              origin: "model",
              authority: "none",
              trust_tier: "trusted",
              content: "plan summary",
              created_at: "t",
              ord: 1,
            },
          ]}
        />
      ));
      await Promise.resolve();
      await Promise.resolve();
      await new Promise(requestAnimationFrame);
      expect(
        transcriptRowHeightForScope(scope, "summary", "collapsed"),
      ).toBe(40);
      const row = container.querySelector<HTMLElement>(
        '.transcript-viewport-row[data-msg-id="summary"]',
      );
      expect(row).toBeTruthy();
      expect(heightReads).toBeLessThan(10);
    } finally {
      restoreRowHeight();
      clearTranscriptRowHeightsForTests();
    }
  });

  it("does not fade in an older page that arrives already baselined", () => {
    clearTranscriptEntryMemory("s-older");
    const row = (i: number): Message => ({
      id: `o-${i}`,
      role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
      content: `prompt ${i}`,
      created_at: "2026-01-01T00:00:00Z",
      ord: i + 1,
    });
    const tail = Array.from({ length: 20 }, (_, i) => row(i + 100));
    const older = Array.from({ length: 60 }, (_, i) => row(i));
    noteTranscriptEntryBaseline("s-older", [...older, ...tail].map((m) => m.id));

    const [messages, setMessages] = createSignal<Message[]>(tail);
    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-older" messages={messages()} />
    ));
    expect(container.querySelector(".den-enter-fade")).toBeNull();

    setMessages([...older, ...tail]);
    expect(container.querySelector(".den-enter-fade")).toBeNull();
    clearTranscriptEntryMemory("s-older");
  });
});
