import { expect } from "@playwright/test";
import { apiSeedSessionTranscript, bootstrapChatSession, liveChatStage, modelIndependentWebE2e, settledChatSessionId, transcriptMessages } from "./helpers.ts";

modelIndependentWebE2e("long activity cards use the enclosing viewport through the last action", async ({ page }) => {
  await page.goto("/e2e/fixtures/chat-content-scroll.html");
  const activity = page.getByTestId("activity-span-card");
  await activity.locator(":scope > summary").click();
  await expect(activity).not.toHaveAttribute("data-animating", "true");
  const list = activity.getByTestId("virtual-card-list");
  await expect(list).toHaveCSS("overflow-y", "visible");
  expect(await list.evaluate(element => element.scrollHeight - element.clientHeight)).toBeLessThan(2);
  expect(await activity.getByTestId("tool-part-card").count()).toBeLessThan(60);
  await page.getByTestId("fixture-scroll").evaluate(element => { element.scrollTop = element.scrollHeight; });
  await expect(activity.locator('[data-tool-call-id="call-9999"]')).toBeVisible();
  await expect(list).toHaveJSProperty("scrollTop", 0);
  expect(await activity.getByTestId("tool-part-card").count()).toBeLessThan(60);
});

modelIndependentWebE2e("tool content opens a Files reader with bounded scrolling through end and resize", async ({ page }, testInfo) => {
  await page.goto("/e2e/fixtures/chat-content-scroll.html");
  const activity = page.getByTestId("activity-span-card");
  await activity.locator(":scope > summary").click();
  expect(await page.getByTestId("tool-part-card").count()).toBeLessThan(40);
  const card = page.locator('[data-testid="tool-part-card"][data-tool-call-id="call-0"]');
  await card.locator(":scope > summary").click();
  await expect.poll(() => card.evaluate(element => {
    const next = element.closest('[data-index]')?.nextElementSibling;
    return next ? next.getBoundingClientRect().top - element.getBoundingClientRect().bottom : -Infinity;
  })).toBeGreaterThanOrEqual(-1);
  expect(await card.locator("[data-den-scrollport]").count()).toBe(0);
  await card.getByRole("button", { name: "Raw output in Files", exact: true }).click();
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
  await card.getByRole("button", { name: "Raw output in Files", exact: true }).click();
  await expect(reader).toContainText("Line 19999:");
  await expect(page.getByTestId("content-request-count")).toHaveText(requests!);
  await page.screenshot({ path: testInfo.outputPath("tool-output-end.png") });
});

modelIndependentWebE2e("approval documents open in Files without approving or adding nested scroll panes", async ({page}) => {
  await page.goto("/e2e/fixtures/chat-content-scroll.html");
  await page.getByRole("button", {name:"Show approvals",exact:true}).click();
  const card = page.getByTestId("tool-approval-card");
  await expect(card.getByTestId("approval-approve-primary")).toBeVisible();
  expect(await card.locator(".den-approval-card-scroll, [data-testid=virtual-card-list]").count()).toBe(0);
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
  await completed.locator("summary").click();
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
