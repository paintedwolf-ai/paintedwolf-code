// Measures what one arriving row costs the main thread once a transcript is long:
// every animation-frame callback is timed in the page while rows stream in through
// the host. Runs against a running `./task den:harness`, using the values its banner
// prints:
//
//   LYCAON_E2E_BASE_URL=<open> LYCAON_E2E_API_URL=<sidecar> LYCAON_E2E_TOKEN=<token> \
//     [PW_STREAMING_COST_REPORT=<file>] [PLAYWRIGHT_WEB_BROWSER=webkit|chromium] \
//     bun scripts/harness/measure-transcript-streaming.ts
//
// The timings are evidence for a review, not a gate.
import { writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { settleTranscriptLayout } from "../../lycaon-den/e2e/fixtures/settle.ts";

const denDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../lycaon-den");
const playwright = createRequire(path.join(denDir, "package.json"))("@playwright/test") as typeof import("@playwright/test");

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
type Role = "user" | "assistant" | "tool";
type Seed = { id: string; role: Role; content: string; seq: number; ord: number; [field: string]: unknown };

function required(name: string): string {
  const value = process.env[name]?.trim();
  if (!value) throw new Error(`Set ${name}`);
  return value;
}

const baseURL = required("LYCAON_E2E_BASE_URL");
const apiUrl = required("LYCAON_E2E_API_URL");
const token = required("LYCAON_E2E_TOKEN");
const browserName = process.env.PLAYWRIGHT_WEB_BROWSER ?? "chromium";
if (browserName !== "webkit" && browserName !== "chromium") throw new Error("PLAYWRIGHT_WEB_BROWSER must be webkit or chromium");
const SEEDED_ROWS = 80;
const STREAMED_ROWS = 40;

const provenance = {
  user: { origin: "user", authority: "user", trust_tier: "trusted" },
  assistant: { origin: "model", authority: "none", trust_tier: "trusted" },
  tool: { origin: "tool", authority: "none", trust_tier: "untrusted" },
} as const;

/** Appends rows through the harness transcript store and its SSE path. */
async function seedTranscript(sessionId: string, seeds: Seed[]): Promise<void> {
  const messages = seeds.map((seed) => ({ created_at: "2026-09-30T10:00:00Z", ...seed, ...provenance[seed.role] }));
  const response = await fetch(`${apiUrl}/harness/transcript`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify({ session_id: sessionId, messages }),
  });
  if (!response.ok) throw new Error(`harness transcript seed: ${response.status} ${await response.text()}`);
}

function seededRows(): Seed[] {
  const history: Seed[] = Array.from({ length: SEEDED_ROWS }, (_, index) => {
    const ord = index + 1;
    return {
      id: crypto.randomUUID(),
      role: ord % 2 === 0 ? "user" : "assistant",
      content: ord % 2 === 0 ? `Seeded user turn ${ord}` : `Seeded assistant reply ${ord} with a few more words so the row wraps once or twice.`,
      seq: ord,
      ord,
    };
  });
  // Two writes to one path: the diff row composes a range and asks the comparison for its summary once.
  const writes = [
    { before: "", after: "export const value = 1;\n" },
    { before: "export const value = 1;\n", after: "export const value = 2;\n" },
  ];
  return [
    ...history,
    ...writes.flatMap((write, index): Seed[] => {
      const assistantId = crypto.randomUUID();
      const toolCallId = `tc-streaming-cost-write-${index}`;
      const seq = SEEDED_ROWS + 1 + index * 2;
      return [
        {
          id: assistantId, role: "assistant", content: "",
          tool_calls: [{ id: toolCallId, name: "write", args: { path: "src/streamed.ts", content: write.after } }],
          seq, ord: seq,
        },
        {
          id: crypto.randomUUID(), role: "tool", content: "wrote src/streamed.ts",
          tool_result: {
            content: "wrote src/streamed.ts", tool: "write", tool_call_id: toolCallId, assistant_message_id: assistantId,
            file_edits: [{ path: "src/streamed.ts", ...write }],
          },
          seq: seq + 1, ord: seq + 1,
        },
      ];
    }),
  ];
}

async function installProbe(page: import("@playwright/test").Page): Promise<void> {
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

type HarnessResult = { ok: boolean; error?: string; state?: { sessionId?: string | null } };

const browser = await playwright[browserName].launch();
let report: Report;
try {
  const page = await browser.newPage();
  await page.goto(baseURL);
  await page.waitForFunction(() => "__harness" in window);
  const opened = await page.evaluate(() => (window as unknown as { __harness: { openProject(name: string): Promise<HarnessResult> } }).__harness.openProject("Harness"));
  if (!opened.ok) throw new Error(`open Harness project: ${opened.error ?? "failed"}`);
  const session = await page.evaluate(() => (window as unknown as { __harness: { newSession(): Promise<HarnessResult> } }).__harness.newSession());
  const sessionId = session.state?.sessionId;
  if (!session.ok || !sessionId) throw new Error(`open a chat: ${session.error ?? "no session id"}`);

  await seedTranscript(sessionId, seededRows());
  await page.getByTestId("file-edit-diff-writes").filter({ hasText: /^2 edits$/ }).waitFor({ state: "attached", timeout: 20_000 });
  await page.getByTestId("file-edit-diff-stat").waitFor({ state: "attached", timeout: 20_000 });
  await settleTranscriptLayout(page);

  await installProbe(page);
  await page.evaluate(() => (window as unknown as { __streamingCost: { start(): void } }).__streamingCost.start());
  const base = SEEDED_ROWS + 4;
  for (let step = 1; step <= STREAMED_ROWS; step += 1) {
    await seedTranscript(sessionId, [{
      id: crypto.randomUUID(), role: "assistant",
      content: `Streamed reply ${step}: ${"piece ".repeat(24).trim()}.`,
      seq: base + step, ord: base + step,
    }]);
    await page.getByTestId("message-stream").getByText(`Streamed reply ${step}:`, { exact: false }).first().waitFor({ state: "attached", timeout: 20_000 });
  }
  // A late refetch would follow the last row's layout.
  await settleTranscriptLayout(page);
  report = await page.evaluate(() => (window as unknown as { __streamingCost: { stop(): Report } }).__streamingCost.stop());
} finally {
  await browser.close();
}

const output = JSON.stringify({ browserName, seededRows: SEEDED_ROWS, streamedRows: STREAMED_ROWS, ...report }, null, 2);
const out = process.env.PW_STREAMING_COST_REPORT;
if (out) writeFileSync(out, output);
console.log(output);
if (report.rowsAdded === 0) {
  console.error("transcript streaming measurement: no streamed row reached the transcript");
  process.exit(1);
}
