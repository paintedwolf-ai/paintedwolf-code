import { expect } from "@playwright/test";
import { apiSeedSessionTranscript, bootstrapChatSession, liveChatStage, modelIndependentWebE2e, settledChatSessionId, transcriptMessages } from "./helpers.ts";

modelIndependentWebE2e("long activity cards use the enclosing viewport through the last action", async ({ page }) => {
  await page.goto("/e2e/fixtures/chat-content-scroll.html");
  const activity = page.getByTestId("activity-span-card");
  await activity.locator(":scope > .den-transcript-disclosure-header").click();
  await expect(activity).not.toHaveAttribute("data-animating", "true");
  const list = activity.getByTestId("virtual-card-list");
  await expect(list).toHaveCSS("overflow-y", "visible");
  expect(await list.evaluate(element => element.scrollHeight - element.clientHeight)).toBeLessThan(2);
  expect(await activity.getByTestId("action-card").count()).toBeLessThan(60);
  await page.getByTestId("fixture-scroll").evaluate(element => { element.scrollTop = element.scrollHeight; });
  await expect(activity.locator('[data-tool-call-id="call-9999"]')).toBeVisible();
  await expect(list).toHaveJSProperty("scrollTop", 0);
  expect(await activity.getByTestId("action-card").count()).toBeLessThan(60);
});

modelIndependentWebE2e("tool content opens a Files reader with bounded scrolling through end and resize", async ({ page }, testInfo) => {
  await page.goto("/e2e/fixtures/chat-content-scroll.html");
  const activity = page.getByTestId("activity-span-card");
  await activity.locator(":scope > .den-transcript-disclosure-header").click();
  expect(await page.getByTestId("action-card").count()).toBeLessThan(40);
  const card = page.locator('[data-testid="action-card"][data-tool-call-id="call-0"]');
  await card.locator(":scope > .den-transcript-disclosure-header").click();
  await expect.poll(() => card.evaluate(element => {
    const next = element.closest('[data-index]')?.nextElementSibling;
    return next ? next.getBoundingClientRect().top - element.getBoundingClientRect().bottom : -Infinity;
  })).toBeGreaterThanOrEqual(-1);
  expect(await card.locator("[data-den-scrollport]").count()).toBe(0);
  await card.getByRole("button", { name: "Output in Files", exact: true }).click();
  const reader = page.getByTestId("chat-content-page");
  const scroller = reader.locator(".cm-scroller");
  await expect(reader).toContainText("Line 0:");
  await expect(page.getByTestId("content-request-count")).toHaveText("0");
  expect(await reader.locator(".cm-content[contenteditable=true]").count()).toBe(0);
  expect(await reader.locator(".cm-scroller").count()).toBe(1);
  await expect(reader.locator(".cm-content")).toHaveAttribute("contenteditable", "false");
  expect(await reader.locator("header").count()).toBe(0);
  await expect(reader.locator(".den-files-toolbar")).toBeVisible();
  await expect(reader.getByRole("button", { name: "Wrap", exact: true })).toBeVisible();
  await expect.poll(()=>reader.locator(".den-files-editor__status-spacer").evaluate(element=>element.getBoundingClientRect().width)).toBeGreaterThan(20);
  await reader.getByRole("button", {name:"Go to line",exact:true}).click();
  await page.getByTestId("goto-line-input").fill("12346:8");
  await page.getByTestId("goto-line-input").press("Enter");
  await expect(reader).toContainText("Line 12345:");
  await expect(reader.locator(".den-files-editor__status")).toContainText("Ln 12346, Col 8");
  await reader.getByRole("button", {name:"Find in file",exact:true}).click();
  await page.getByTestId("find-bar-input").fill("Line 19000:");
  await expect(reader).toContainText("Line 19000:");
  await page.getByRole("button", {name:"Close find",exact:true}).click();
  await scroller.hover();
  await page.mouse.wheel(0, 1000000);
  await expect(reader).toContainText("Line 19999:");
  const endGap = () => scroller.evaluate(element => {
    const last = element.querySelector('[data-source-row="19999"]')!;
    return (element.getBoundingClientRect().bottom - last.getBoundingClientRect().bottom) / last.getBoundingClientRect().height;
  });
  await scroller.hover();
  await page.mouse.wheel(0, 100000);
  await expect.poll(endGap).toBeLessThan(2.5);
  for (const width of ["Narrow", "Wide"]) {
    await page.getByRole("button", { name: width, exact: true }).click();
    await scroller.hover();
    await page.mouse.wheel(0, 100000);
    await expect(reader).toContainText("Line 19999:");
    await expect.poll(endGap).toBeLessThan(2.5);
  }
  expect(await reader.locator(".cm-line").count()).toBeLessThan(100);
  const requests = await page.getByTestId("content-request-count").textContent();
  await card.getByRole("button", { name: "Output in Files", exact: true }).click();
  await expect(reader).toContainText("Line 19999:");
  await expect(page.getByTestId("content-request-count")).toHaveText(requests!);
  await page.screenshot({ path: testInfo.outputPath("tool-output-end.png") });
});

modelIndependentWebE2e("approval documents open in Files without approving or adding nested scroll panes", async ({page}) => {
  await page.goto("/e2e/fixtures/chat-content-scroll.html");
  await page.getByRole("button", {name:"Show approvals",exact:true}).click();
  const card = page.getByTestId("tool-approval-card");
  await expect(card.getByTestId("approval-approve-primary")).toBeVisible();
  // The card scrolls its subject and context once; long target lists open in Files.
  await expect(card.locator(".den-approval-card-scroll")).toHaveCount(1);
  await expect(card.getByTestId("virtual-card-list")).toHaveCount(0);
  const targets = card.getByRole("button", {name:"Show all 1000 destinations in Files",exact:true});
  await targets.focus();
  await targets.press("Enter");
  await expect(page.getByTestId("approval-decision-count")).toHaveText("0");
  const reader = page.getByTestId("chat-content-page");
  await expect(reader).toContainText("1. host-0.example");
  await reader.getByRole("button", {name:"Go to line",exact:true}).click();
  await page.getByTestId("goto-line-input").fill("1999");
  await page.getByTestId("goto-line-input").press("Enter");
  await expect(reader).toContainText("1000. host-999.example");
  const completed = page.getByTestId("checkpoint-decision-chicklet");
  await completed.locator(".den-transcript-disclosure-header").click();
  await completed.getByRole("button", {name:"Approval details in Files",exact:true}).click();
  await expect(reader).toContainText("Preserve the build manifest");
  await expect(page.getByTestId("approval-decision-count")).toHaveText("0");
});

modelIndependentWebE2e("wide Markdown tables scroll locally while chat stays vertical", async ({ page, request }) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  const columns = Array.from({ length: 14 }, (_, index) => `Column ${index}`);
  const table = [
    `| ${columns.join(" | ")} |`,
    `| ${columns.map(() => "---").join(" | ")} |`,
    `| ${columns.map((_, index) => `measurement_${index}_123456789`).join(" | ")} |`,
  ].join("\n");
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages([
    { id: crypto.randomUUID(), role: "user", content: "Show the measurements", ord: 1, created_at: "2026-09-17T12:00:00Z" },
    { id: crypto.randomUUID(), role: "assistant", content: table, ord: 2, created_at: "2026-09-17T12:01:00Z" },
  ]));
  const stream = liveChatStage(page).locator(".den-chat-stream");
  const region = stream.getByRole("region", { name: "Table", exact: true });
  await expect(region).toBeVisible();
  for (const width of [1200, 760]) {
    await page.setViewportSize({ width, height: 900 });
    await expect(stream).toHaveCSS("overflow-x", "hidden");
    await expect.poll(() => stream.evaluate(element => element.scrollWidth - element.clientWidth)).toBeLessThan(2);
    await expect.poll(() => region.evaluate(element => element.scrollWidth - element.clientWidth)).toBeGreaterThan(100);
    await region.evaluate(element => { element.scrollLeft = 0; });
    await region.hover();
    await page.mouse.wheel(400, 0);
    await expect.poll(() => region.evaluate(element => element.scrollLeft)).toBeGreaterThan(0);
    await expect(stream).toHaveJSProperty("scrollLeft", 0);
    await region.focus();
    await expect(region).toBeFocused();
  }
});

modelIndependentWebE2e("plain arrows scroll a focused conversation row normally", async ({ page, request }) => {
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages(
    Array.from({ length: 30 }, (_, index) => ({
      id: crypto.randomUUID(), role: index % 2 ? "assistant" as const : "user" as const,
      content: `Message ${index + 1}. ${"Conversation text for reading. ".repeat(60)}`,
      created_at: "2026-10-23T12:00:00Z", seq: index + 1, ord: index + 1,
    })),
  ));
  const stream = liveChatStage(page).locator(".den-chat-stream");
  const rows = stream.locator(".transcript-viewport-row[data-msg-id]");
  await expect(rows.first()).toBeAttached();
  await stream.hover();
  await page.mouse.wheel(0, -100_000);
  await expect.poll(() => stream.evaluate(element => element.scrollTop)).toBeLessThan(2);
  await expect(rows.first()).toBeVisible();
  // Message rows are accessibility focus targets. Exercise native keyboard
  // scrolling from that focus state in each browser.
  await rows.first().focus();
  const start = await stream.evaluate(element => element.scrollTop);
  await page.keyboard.press("ArrowDown");
  await expect.poll(() => stream.evaluate(element => element.scrollTop)).toBeGreaterThan(start + 5);
  await expect.poll(() => stream.evaluate(element => element.scrollTop)).toBeLessThan(start + 200);
  await page.keyboard.press("ArrowUp");
  await expect.poll(() => stream.evaluate(element => element.scrollTop)).toBeLessThan(5);
});

type ReaderPosition = { row: number; column: number };
declare global {
  interface Window {
    prettyReader: {
      positionAtTop(): ReaderPosition;
      distance(target: ReaderPosition): number | null;
      readAwayFromCaret(target: ReaderPosition): boolean;
      caretVisible(): boolean;
    };
  }
}

for (const source of ["retained", "receipts"]) {
  modelIndependentWebE2e(`Pretty reader swaps keep the visible character in ${source} output`, async ({ page }) => {
    await page.goto(`/e2e/fixtures/chat-content-scroll.html?pretty=${source}`);
    const reader = page.getByTestId("chat-content-page");
    await reader.getByTestId("files-editor-layout-wrap").click();
    await reader.getByRole("button", { name: "Go to line" }).click();
    await page.getByTestId("goto-line-input").fill("173:330");
    await page.getByTestId("goto-line-input").press("Enter");
    await expect(reader.locator(".cm-content")).toContainText('"row":172');
    await expect.poll(async () => {
      return page.evaluate(async () => {
        if (!window.prettyReader.readAwayFromCaret({ row: 172, column: 330 })) return false;
        await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
        return !window.prettyReader.caretVisible() && window.prettyReader.positionAtTop().row === 172;
      });
    }).toBe(true);
    for (const layout of ["pretty", "wrap"]) {
      await expect.poll(() => page.evaluate(() => window.prettyReader.caretVisible())).toBe(false);
      await expect.poll(() => page.evaluate(() => window.prettyReader.positionAtTop().row)).toBeGreaterThan(95);
      const anchor = await page.evaluate(() => window.prettyReader.positionAtTop());
      const before = await page.evaluate(target => window.prettyReader.distance(target), anchor);
      expect(before).not.toBeNull();
      await reader.getByTestId(`files-editor-layout-${layout}`).click();
      await expect(reader.locator(".cm-den-reader")).toHaveCount(layout === "pretty" ? 0 : 1);
      await expect.poll(() => page.evaluate(async ({ target, expected }) => {
        let largest = 0;
        for (let frame = 0; frame < 5; frame++) {
          await new Promise(requestAnimationFrame);
          const distance = window.prettyReader.distance(target);
          if (distance === null) return Number.POSITIVE_INFINITY;
          largest = Math.max(largest, Math.abs(distance - expected));
        }
        return largest;
      }, { target: anchor, expected: before! }), {
        message: `${layout} keeps row ${anchor.row} column ${anchor.column} at the viewport edge`,
      }).toBeLessThanOrEqual(1);
      await expect.poll(() => page.evaluate(() => window.prettyReader.caretVisible())).toBe(false);
    }
  });
}
