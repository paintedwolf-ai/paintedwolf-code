import { expect, type Page } from "@playwright/test";
import { apiSeedSessionTranscript, apiUploadAttachment, bootstrapChatSession, liveChatStage, modelIndependentWebE2e, settledChatSessionId, transcriptMessages } from "./helpers.ts";

const count = 900;
const body = "hidden PDF extraction ".repeat(750);
const messages = transcriptMessages(Array.from({ length: count }, (_, index) => {
  const attached = index % 10 === 0 || index === count - 1;
  const prose = `Step ${index + 1}. Review this document.`;
  return { id: crypto.randomUUID(), role: attached || index % 2 === 0 ? "user" as const : "assistant" as const,
    content: attached ? prose + body : `Step ${index + 1}.`, seq: index + 1, ord: index + 1,
    created_at: new Date(Date.UTC(2026, 8, 1, 12, 0, index)).toISOString(),
    ...(attached ? { content_parts: [
      { content: prose, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const },
      { content: body, origin: "attachment" as const, authority: "none" as const, trust_tier: "untrusted" as const, source: "report.pdf", media_type: "application/pdf", blob_id: "fixture-pdf", size_bytes: body.length },
    ] } : {}),
  };
}));

const stream = (page: Page) => liveChatStage(page).locator(".den-chat-stream");
async function visibleSteps(page: Page) {
  return stream(page).evaluate(host => {
    const box = host.getBoundingClientRect();
    return [...host.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-msg-id]")]
      .filter(row => row.getBoundingClientRect().bottom > box.top && row.getBoundingClientRect().top < box.bottom)
      .map(row => Number(row.textContent?.match(/Step (\d+)\./)?.[1])).filter(Number.isFinite).sort((a,b) => a-b);
  });
}
async function wheel(page: Page, delta: number) {
  const box = await stream(page).boundingBox();
  if (!box) throw new Error("transcript scrollport missing");
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.wheel(0, delta);
  await page.waitForTimeout(120);
}

modelIndependentWebE2e("PDF chips predict visible geometry and preserve cold-cache history traversal", async ({ page, request }) => {
  modelIndependentWebE2e.setTimeout(600_000);
  const project = await bootstrapChatSession(page, request, { debug: { verboseMode: false } });
  // Real blob admission keeps seeded attachment references valid in the transcript store.
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
  pdf += `xref\n0 5\n0000000000 65535 f \n${offsets.map(offset => `${String(offset).padStart(10, "0")} 00000 n \n`).join("")}trailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`;
  const upload = await apiUploadAttachment(request, project.id, "report.pdf", "application/pdf", Buffer.from(pdf));
  const seeded = messages.map(message => ({ ...message, content_parts: message.content_parts?.map(part => part.origin === "attachment" ? { ...part, blob_id: upload.blob_id } : part) }));
  const sessionId = await settledChatSessionId(page);
  for (let offset = 0; offset < seeded.length; offset += 60) {
    await apiSeedSessionTranscript(request, sessionId, seeded.slice(offset, offset + 60));
  }
  const tail = seeded.at(-1)!;
  const row = stream(page).locator(`.transcript-viewport-row[data-msg-id="${tail.id}"]`);
  await expect(row).toBeAttached({ timeout: 30_000 });
  await expect(row.getByTestId("transcript-attachment-chip")).toHaveText(/report.pdf/);
  await expect(row).not.toContainText("hidden PDF extraction");
  for (const width of [632, 898, 1129]) {
    const inner = liveChatStage(page).locator(".den-chat-stream-inner");
    const current = await inner.evaluate(el => el.getBoundingClientRect().width);
    const viewport = page.viewportSize()!;
    await page.setViewportSize({ width: Math.round(viewport.width + width - current), height: 900 });
    await expect.poll(() => inner.evaluate((el, expectedWidth) => Math.abs(el.getBoundingClientRect().width - expectedWidth), width)).toBeLessThan(2);
    const geometry = await row.evaluate(async (element, message) => {
      const column = element.closest<HTMLElement>(".den-chat-stream-inner")!;
      const path = "/src/chat/transcript/layout/transcript-row-content-estimate.ts";
      const estimates = await import(/* @vite-ignore */ path) as typeof import("../src/chat/transcript/layout/transcript-row-content-estimate.ts");
      const spacingPath = "/src/chat/transcript/layout/transcript-spacing.ts";
      const spacing = await import(/* @vite-ignore */ spacingPath) as typeof import("../src/chat/transcript/layout/transcript-spacing.ts");
      const metrics = { widthPx: column.getBoundingClientRect().width, remPx: parseFloat(getComputedStyle(document.documentElement).fontSize), bodyPx: parseFloat(getComputedStyle(column).fontSize), spacing: spacing.transcriptSpacing() };
      const estimate = estimates.transcriptRowContentEstimate({ kind: "user", key: message.id, text: message.content, contentParts: message.content_parts }, metrics, [])!;
      const rendered = element.querySelector<HTMLElement>(".bubble--user")!.getBoundingClientRect().height;
      return { estimate, rendered };
    }, tail);
    expect(geometry.estimate).toBeLessThan(150);
    expect(Math.abs(geometry.estimate - geometry.rendered)).toBeLessThan(Math.max(12, geometry.rendered * .25));
  }
  await expect(async () => {
    await wheel(page, -3000);
    expect(Math.min(...await visibleSteps(page))).toBeLessThanOrEqual(150);
  }).toPass({ timeout: 120_000 });
  let previous = await visibleSteps(page);
  await expect(async () => {
    await wheel(page, 500);
    const steps = await visibleSteps(page);
    expect(steps.length).toBeGreaterThan(0);
    for (let i = 1; i < steps.length; i++) expect(steps[i]).toBe(steps[i-1]! + 1);
    expect(Math.min(...steps)).toBeLessThanOrEqual(Math.max(...previous) + 1);
    previous = steps;
    expect(Math.max(...steps)).toBe(count);
  }).toPass({ timeout: 180_000 });
  await expect(row).toBeVisible();
});
