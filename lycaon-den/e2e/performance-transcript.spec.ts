import { writeFileSync } from "node:fs";
import { expect } from "@playwright/test";
import { activateProject, apiSeedSessionTranscript, bootstrapChatSession, modelIndependentWebE2e as test, settledChatSessionId, transcriptMessages } from "./helpers.ts";

test("republishes settled transcript geometry across resize and restoration", async ({ page, request }, info) => {
  const project = await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  const messages = transcriptMessages(Array.from({ length: 6 }, (_, index) => ({
    id: crypto.randomUUID(), role: (index % 3 === 2 ? "assistant" : "system") as "assistant" | "system",
    content: index % 3 === 2 ? `# Report ${index}\n\n` + "| Item | Result |\n| --- | --- |\n" + "| Check | Complete |\n".repeat(12) : "",
    ...(index % 3 === 2 ? {} : { kind: "progress_complete" as const, progress_complete: { seq: index + 1, steps: Array.from({ length: 6 }, (_, step) => ({ label: `Verified step ${step + 1}`, state: "done" as const })) } }),
    seq: index + 1, ord: index + 1, created_at: "2026-09-15T00:00:00Z",
  })));
  await apiSeedSessionTranscript(request, sessionId, messages);
  await expect(page.locator(".transcript-viewport-row[data-msg-id]").first()).toBeAttached();
  const samples: unknown[] = [];
  const capture = async (label: string) => {
    await page.evaluate(async () => { for (let n = 0; n < 30; n++) await new Promise(requestAnimationFrame); });
    const rows = await page.evaluate(async ({ projectId, sessionId }) => {
      const moduleUrl = "/src/chat/transcript/layout/transcript-row-heights-persist.ts";
      const heights = await import(/* @vite-ignore */ moduleUrl) as typeof import("../src/chat/transcript/layout/transcript-row-heights-persist.ts");
      const host = [...document.querySelectorAll<HTMLElement>(".den-chat-stream")].find(element => !element.closest('[data-resident="idle"]') && element.clientHeight > 0)!;
      // The cache holds row interiors; a row's seam is padding above its interior.
      const seamPx = (element: HTMLElement) => element.dataset.seam ? Number.parseFloat(getComputedStyle(element).paddingTop) : 0;
      return [...host.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-msg-id]")].map(element => ({
        id: element.dataset.msgId!, key: element.dataset.rowHeightKey, measured: element.getBoundingClientRect().height - seamPx(element),
        cached: heights.transcriptRowHeightForScope({ projectId, sessionId }, element.dataset.msgId!, element.dataset.rowHeightKey ?? ""),
      }));
    }, { projectId: project.id, sessionId });
    expect(rows.length).toBeGreaterThan(0);
    for (const row of rows) {
      expect(row.cached, `${label}: ${row.id} was not published`).toBeDefined();
      expect(Math.abs(row.cached! - row.measured), `${label}: ${row.id} geometry differs`).toBeLessThan(1);
    }
    samples.push({ label, rows });
  };
  await capture("initial");
  await page.setViewportSize({ width: 1000, height: 800 });
  await capture("narrow");
  await page.setViewportSize({ width: 1600, height: 800 });
  await capture("wide");
  await page.getByTestId("nav-brand").click();
  await activateProject(page, project.id);
  await expect(page.locator(".transcript-viewport-row[data-msg-id]").first()).toBeAttached();
  await capture("reopened");
  writeFileSync(info.outputPath("transcript-geometry.json"), JSON.stringify(samples, null, 2));
});
