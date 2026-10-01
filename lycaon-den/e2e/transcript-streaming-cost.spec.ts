import { writeFileSync } from "node:fs";
import { expect, type Page } from "@playwright/test";
import {
  apiSeedSessionTranscript,
  bootstrapChatSession,
  settledChatSessionId,
  transcriptMessages,
  webE2e,
} from "./helpers.ts";

/**
 * Measures what one arriving row costs the main thread once a transcript is long: every
 * animation-frame callback is timed in the page while rows stream in through the host, and
 * the report lands in `PW_STREAMING_COST_REPORT` when that path is set. The assertions hold
 * only the parts that must never regress; the timings are evidence for a review, not a gate.
 */

type Report = {
  rafCallbacks: number;
  busyCallbacks: number;
  busyTotalMs: number;
  p50: number;
  p90: number;
  max: number;
  over16: number;
  over50: number;
  sourceViewGets: number;
  mutations: number;
  rowsAdded: number;
};

const SEEDED_ROWS = 80;
const STREAMED_ROWS = 40;

async function installProbe(page: Page): Promise<void> {
  await page.evaluate(() => {
    const state = {
      durations: [] as number[], sourceViewGets: 0, mutations: 0, rowsAdded: 0, running: false,
    };
    const nativeRaf = window.requestAnimationFrame.bind(window);
    window.requestAnimationFrame = (cb) => nativeRaf((t) => {
      if (!state.running) return cb(t);
      const start = performance.now();
      try { return cb(t); } finally { state.durations.push(performance.now() - start); }
    });
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        if (state.running && /\/source\/views\/[0-9a-f-]+(\?|$)/.test(entry.name)) state.sourceViewGets += 1;
      }
    }).observe({ entryTypes: ["resource"] });
    const stream = document.querySelector('[data-testid="message-stream"]');
    new MutationObserver((records) => {
      if (!state.running) return;
      state.mutations += records.length;
      for (const record of records) {
        for (const node of record.addedNodes) {
          if (node instanceof HTMLElement && node.classList.contains("transcript-viewport-row")) state.rowsAdded += 1;
        }
      }
    }).observe(stream ?? document.body, { subtree: true, childList: true, attributes: true, characterData: true });
    (window as unknown as { __streamingCost: unknown }).__streamingCost = {
      start: () => { state.durations = []; state.sourceViewGets = 0; state.mutations = 0; state.rowsAdded = 0; state.running = true; },
      stop: (): Report => {
        state.running = false;
        const sorted = [...state.durations].sort((a, b) => a - b);
        const busy = sorted.filter((ms) => ms >= 2);
        const at = (q: number) => Math.round((busy[Math.min(busy.length - 1, Math.floor(busy.length * q))] ?? 0) * 10) / 10;
        return {
          rafCallbacks: sorted.length, busyCallbacks: busy.length,
          busyTotalMs: Math.round(busy.reduce((sum, ms) => sum + ms, 0)),
          p50: at(0.5), p90: at(0.9), max: at(1),
          over16: busy.filter((ms) => ms > 16).length, over50: busy.filter((ms) => ms > 50).length,
          sourceViewGets: state.sourceViewGets, mutations: state.mutations, rowsAdded: state.rowsAdded,
        };
      },
    };
  });
}

webE2e("streaming rows into a long transcript: main-thread cost and request count", async ({ page, request }) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  const history = Array.from({ length: SEEDED_ROWS }, (_, index) => {
    const ord = index + 1;
    return {
      id: crypto.randomUUID(),
      role: ord % 2 === 0 ? ("user" as const) : ("assistant" as const),
      content: ord % 2 === 0 ? `Seeded user turn ${ord}` : `Seeded assistant reply ${ord} with a few more words so the row wraps once or twice.`,
      created_at: "2026-09-30T10:00:00Z",
      seq: ord,
      ord,
    };
  });
  // Two writes to one path: the diff row composes a range and asks the comparison for its summary once.
  const writes = [
    { before: "", after: "export const value = 1;\n" },
    { before: "export const value = 1;\n", after: "export const value = 2;\n" },
  ];
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages([
    ...history,
    ...writes.flatMap((write, index) => {
      const assistantId = crypto.randomUUID();
      const toolCallId = `tc-streaming-cost-write-${index}`;
      const seq = SEEDED_ROWS + 1 + index * 2;
      return [
        {
          id: assistantId, role: "assistant" as const, content: "",
          tool_calls: [{ id: toolCallId, name: "write", args: { path: "src/streamed.ts", content: write.after } }],
          created_at: "2026-09-30T10:01:00Z", seq, ord: seq,
        },
        {
          id: crypto.randomUUID(), role: "tool" as const, content: "wrote src/streamed.ts",
          tool_result: {
            content: "wrote src/streamed.ts", tool: "write", tool_call_id: toolCallId, assistant_message_id: assistantId,
            file_edit: { path: "src/streamed.ts", ...write },
          },
          created_at: "2026-09-30T10:01:01Z", seq: seq + 1, ord: seq + 1,
        },
      ];
    }),
  ]));
  await expect(page.getByTestId("file-edit-diff")).toBeAttached({ timeout: 20_000 });
  await expect(page.getByTestId("file-edit-diff-writes")).toHaveText("2 edits", { timeout: 20_000 });
  await expect(page.getByTestId("file-edit-diff-stat")).toBeAttached({ timeout: 20_000 });
  await page.waitForTimeout(1_000);

  await installProbe(page);
  await page.evaluate(() => (window as unknown as { __streamingCost: { start(): void } }).__streamingCost.start());
  const base = SEEDED_ROWS + 4;
  for (let step = 1; step <= STREAMED_ROWS; step += 1) {
    await apiSeedSessionTranscript(request, sessionId, transcriptMessages([
      {
        id: crypto.randomUUID(), role: "assistant",
        content: `Streamed reply ${step}: ${"piece ".repeat(24).trim()}.`,
        created_at: "2026-09-30T10:02:00Z", seq: base + step, ord: base + step,
      },
    ]));
    await page.waitForTimeout(100);
  }
  await expect(page.getByTestId("message-stream").getByText(`Streamed reply ${STREAMED_ROWS}:`, { exact: false }).first()).toBeAttached({ timeout: 20_000 });
  await page.waitForTimeout(500);
  const report = await page.evaluate(() => (window as unknown as { __streamingCost: { stop(): Report } }).__streamingCost.stop());

  const out = process.env.PW_STREAMING_COST_REPORT;
  if (out) writeFileSync(out, JSON.stringify({ seededRows: SEEDED_ROWS, streamedRows: STREAMED_ROWS, ...report }, null, 2));
  // A settled diff row never asks for its comparison again while other rows arrive.
  expect(report.sourceViewGets, JSON.stringify(report)).toBe(0);
  expect(report.rowsAdded, JSON.stringify(report)).toBeGreaterThan(0);
});
