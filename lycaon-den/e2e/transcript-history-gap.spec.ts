import { expect, type Page, type Route } from "@playwright/test";
import {
  apiSeedSessionTranscript,
  apiUploadAttachment,
  bootstrapChatSession,
  liveChatStage,
  modelIndependentWebE2e,
  settledChatSessionId,
  transcriptMessages,
} from "./helpers.ts";

const SCROLLER = ".den-chat-stream";
const ROWS = 900;

function steps(count: number) {
  return Array.from({ length: count }, (_, index) => ({
    id: crypto.randomUUID(),
    role: index % 2 === 0 ? ("user" as const) : ("assistant" as const),
    content: `Step ${index + 1}.`,
    created_at: new Date(Date.UTC(2026, 8, 1, 12, 0, index)).toISOString(),
    seq: index + 1,
    ord: index + 1,
  }));
}

function conversation(count: number) {
  return transcriptMessages(steps(count));
}

const PDF_EXTRACTION = "hidden PDF extraction ".repeat(750);

/** Every tenth row, and the tail, is a user row whose PDF extraction the bubble hides behind a chip. */
function attachmentConversation(count: number, blobId: string) {
  return transcriptMessages(steps(count).map((step, index) => {
    if (index % 10 !== 0 && index !== count - 1) return step;
    const prose = `Step ${index + 1}. Review this document.`;
    return {
      ...step,
      role: "user" as const,
      content: prose + PDF_EXTRACTION,
      content_parts: [
        { content: prose, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const },
        {
          content: PDF_EXTRACTION, origin: "attachment" as const, authority: "none" as const, trust_tier: "untrusted" as const,
          source: "report.pdf", media_type: "application/pdf", blob_id: blobId, size_bytes: PDF_EXTRACTION.length,
        },
      ],
    };
  }));
}

/** A minimal one-page PDF, so seeded attachment references resolve through real blob admission. */
function onePagePdf(): Buffer {
  const objects = [
    "<< /Type /Catalog /Pages 2 0 R >>",
    "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
    "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R >>",
    "<< /Length 0 >>\nstream\n\nendstream",
  ];
  let pdf = "%PDF-1.4\n";
  const offsets = objects.map((object, i) => {
    const offset = Buffer.byteLength(pdf);
    pdf += `${i + 1} 0 obj\n${object}\nendobj\n`;
    return offset;
  });
  const xref = Buffer.byteLength(pdf);
  pdf += `xref\n0 5\n0000000000 65535 f \n${offsets.map((offset) => `${String(offset).padStart(10, "0")} 00000 n \n`).join("")}`;
  pdf += `trailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`;
  return Buffer.from(pdf);
}

const scroller = (page: Page) => liveChatStage(page).locator(SCROLLER);

/** Step numbers of the rows the scrollport shows, in order. */
async function visibleSteps(page: Page): Promise<number[]> {
  return scroller(page).evaluate((host) => {
    const view = host.getBoundingClientRect();
    return [...host.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-msg-id]")]
      .filter((row) => {
        const box = row.getBoundingClientRect();
        return box.bottom > view.top && box.top < view.bottom;
      })
      .map((row) => Number(row.textContent?.match(/Step (\d+)\./)?.[1] ?? Number.NaN))
      .filter((step) => Number.isFinite(step))
      .sort((a, b) => a - b);
  });
}

/** The first row under the scrollport top and where it sits. */
async function readingRow(page: Page): Promise<{ key: string; top: number }> {
  return scroller(page).evaluate((host) => {
    const line = host.getBoundingClientRect().top;
    for (const row of host.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-msg-id]")) {
      const box = row.getBoundingClientRect();
      if (box.bottom > line) return { key: row.dataset.msgId ?? "", top: box.top - line };
    }
    throw new Error("No transcript row crosses the scrollport top.");
  });
}

async function rowTop(page: Page, key: string): Promise<number | undefined> {
  return scroller(page).evaluate((host, msgId) => {
    const row = host.querySelector<HTMLElement>(`.transcript-viewport-row[data-msg-id="${CSS.escape(msgId)}"]`);
    return row ? row.getBoundingClientRect().top - host.getBoundingClientRect().top : undefined;
  }, key);
}

async function wheel(page: Page, deltaY: number) {
  const box = await scroller(page).boundingBox();
  if (!box) throw new Error("chat scroller is missing");
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.wheel(0, deltaY);
  await page.waitForTimeout(120);
}

function expectContiguous(steps: readonly number[]) {
  for (let i = 1; i < steps.length; i++) expect(steps[i], `rows ${steps.join(",")}`).toBe((steps[i - 1] ?? 0) + 1);
}

/** Wheel into history until the scrollport shows rows at or before `step`. */
async function readBackTo(page: Page, step: number) {
  await expect(async () => {
    await wheel(page, -3_000);
    expect(Math.min(...(await visibleSteps(page)))).toBeLessThanOrEqual(step);
  }).toPass({ timeout: 120_000 });
}

/** Wheel down, proving each screen continues the one before it. */
async function readForward(page: Page, until: () => Promise<boolean>) {
  let previous = await visibleSteps(page);
  await expect(async () => {
    await wheel(page, 500);
    const steps = await visibleSteps(page);
    expectContiguous(steps);
    // Reading down never skips a row the reader has not seen.
    expect(Math.min(...steps)).toBeLessThanOrEqual(Math.max(...previous) + 1);
    previous = steps;
    const done = await until();
    expect(done).toBe(true);
  }).toPass({ timeout: 180_000 });
}

modelIndependentWebE2e("reading down from deep history reaches the live tail without skipping rows", async ({ page }) => {
  modelIndependentWebE2e.setTimeout(600_000);
  await bootstrapChatSession(page);
  const sessionId = await settledChatSessionId(page);
  // The harness seeds at most a thousand rows per request.
  const rows = conversation(ROWS);
  await apiSeedSessionTranscript(page.request, sessionId, rows.slice(0, 450));
  await apiSeedSessionTranscript(page.request, sessionId, rows.slice(450));
  await expect(liveChatStage(page).getByText(`Step ${ROWS}.`, { exact: true })).toBeVisible({ timeout: 30_000 });

  // Six hundred rows fit resident at once; reading back seven hundred fifty cuts history off from the tail.
  await readBackTo(page, 150);
  expectContiguous(await visibleSteps(page));

  // Hold the page that leads back toward the tail to inspect the end of presented history.
  const newerPage = (url: URL) =>
    url.pathname.endsWith(`/v1/sessions/${sessionId}/messages`) && url.searchParams.has("after_message_id");
  let held = false;
  let releaseHeld = () => {};
  await page.route(newerPage, async (route: Route) => {
    if (held) return route.continue();
    held = true;
    await new Promise<void>((release) => { releaseHeld = release; });
    await route.continue();
  });
  await readForward(page, async () => held);
  // The end of history before the gap is not the latest message.
  await expect(page.getByTestId("stream-scroll-jump")).toHaveAttribute("aria-hidden", "false");
  const before = await readingRow(page);
  const response = page.waitForResponse((res) => res.url().includes(`/v1/sessions/${sessionId}/messages`) && res.url().includes("after_message_id="));
  releaseHeld();
  await response;
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  const after = await rowTop(page, before.key);
  expect(after, "the reading row stays mounted").toBeDefined();
  expect(Math.abs((after ?? 0) - before.top)).toBeLessThanOrEqual(2);
  await page.unroute(newerPage);

  await readForward(page, async () => (await visibleSteps(page)).includes(ROWS));
  expectContiguous(await visibleSteps(page));

  // Jumping to latest from deep history drops the cut-off pages and lands on the tail.
  await readBackTo(page, 150);
  await page.getByTestId("stream-scroll-jump").click();
  await expect(liveChatStage(page).getByText(`Step ${ROWS}.`, { exact: true })).toBeVisible({ timeout: 30_000 });
  await expect(page.getByTestId("stream-scroll-jump")).toHaveAttribute("aria-hidden", "true");
  const tail = await visibleSteps(page);
  expectContiguous(tail);
  expect(Math.max(...tail)).toBe(ROWS);
});

modelIndependentWebE2e("PDF chips predict visible geometry and preserve cold-cache history traversal", async ({ page, request }) => {
  modelIndependentWebE2e.setTimeout(600_000);
  const project = await bootstrapChatSession(page, request, { debug: { verboseMode: false } });
  const upload = await apiUploadAttachment(request, project.id, "report.pdf", "application/pdf", onePagePdf());
  const rows = attachmentConversation(ROWS, upload.blob_id);
  const sessionId = await settledChatSessionId(page);
  for (let offset = 0; offset < rows.length; offset += 60) {
    await apiSeedSessionTranscript(request, sessionId, rows.slice(offset, offset + 60));
  }
  const tail = rows.at(-1)!;
  const row = scroller(page).locator(`.transcript-viewport-row[data-msg-id="${tail.id}"]`);
  await expect(row).toBeAttached({ timeout: 30_000 });
  await expect(row.getByTestId("transcript-attachment-chip")).toHaveText(/report.pdf/);
  await expect(row).not.toContainText("hidden PDF extraction");

  for (const width of [632, 898, 1129]) {
    const inner = liveChatStage(page).locator(".den-chat-stream-inner");
    const current = await inner.evaluate((el) => el.getBoundingClientRect().width);
    const viewport = page.viewportSize()!;
    await page.setViewportSize({ width: Math.round(viewport.width + width - current), height: 900 });
    await expect.poll(() => inner.evaluate((el, expectedWidth) => Math.abs(el.getBoundingClientRect().width - expectedWidth), width)).toBeLessThan(2);
    const geometry = await row.evaluate(async (element, message) => {
      const column = element.closest<HTMLElement>(".den-chat-stream-inner")!;
      const path = "/src/chat/transcript/layout/transcript-row-content-estimate.ts";
      const estimates = await import(/* @vite-ignore */ path) as typeof import("../src/chat/transcript/layout/transcript-row-content-estimate.ts");
      const spacingPath = "/src/chat/transcript/layout/transcript-spacing.ts";
      const spacing = await import(/* @vite-ignore */ spacingPath) as typeof import("../src/chat/transcript/layout/transcript-spacing.ts");
      const metrics = {
        widthPx: column.getBoundingClientRect().width,
        remPx: parseFloat(getComputedStyle(document.documentElement).fontSize),
        bodyPx: parseFloat(getComputedStyle(column).fontSize),
        spacing: spacing.transcriptSpacing(),
      };
      const estimate = estimates.transcriptRowContentEstimate({ kind: "user", key: message.id, text: message.content, contentParts: message.content_parts }, metrics, [])!;
      const rendered = element.querySelector<HTMLElement>(".bubble--user")!.getBoundingClientRect().height;
      return { estimate, rendered };
    }, tail);
    expect(geometry.estimate).toBeLessThan(150);
    expect(Math.abs(geometry.estimate - geometry.rendered)).toBeLessThan(Math.max(12, geometry.rendered * 0.25));
  }

  await readBackTo(page, 150);
  await readForward(page, async () => (await visibleSteps(page)).includes(ROWS));
  await expect(row).toBeVisible();
});
