import { expect, test, type Page, type Route } from "@playwright/test";
import { bootstrapChatSession, liveChatStage, webE2e } from "./helpers.ts";

const IDEA = "Build a tic tac game with AI I can beat";

/** Holds every matching request until released, as a slow or busy engine would. */
async function hold(page: Page, pattern: string, method: string): Promise<{ release: () => void; count: () => number }> {
  let release!: () => void;
  const released = new Promise<void>((resolve) => { release = resolve; });
  let count = 0;
  await page.route(pattern, async (route: Route) => {
    if (route.request().method() !== method) return route.fallback();
    count++;
    await released;
    await route.fallback();
  });
  return { release, count: () => count };
}

/** Counts first-prompt sends by operation so a duplicate turn is visible. */
function countPrompts(page: Page): () => Map<string, number> {
  const operations = new Map<string, number>();
  page.on("request", (request) => {
    if (request.method() !== "POST" || !/\/v1\/sessions\/[^/]+\/prompts$/.test(request.url())) return;
    const id = (request.postDataJSON() as { operation_id?: string } | null)?.operation_id ?? "";
    operations.set(id, (operations.get(id) ?? 0) + 1);
  });
  return () => operations;
}

async function goHome(page: Page) {
  await page.getByRole("button", { name: "Painted Wolf Code", exact: true }).click();
}

function ideaInput(page: Page) {
  return page.getByRole("textbox", { name: "Describe your idea…", exact: true });
}

webE2e("starting an idea reveals the conversation after a collapsed project workspace", async ({ page, request }) => {
  await page.setViewportSize({ width: 1500, height: 900 });
  await bootstrapChatSession(page, request);
  await page.getByRole("region", { name: "Project context", exact: true })
    .getByRole("button", { name: "Files", exact: true }).click();
  await page.getByTestId("layout-dock-btn").click();
  await page.getByTestId("layout-tile-split").click();
  await page.getByTestId("layout-dock-btn").click();
  await page.getByTestId("conversation-collapse-btn").click();
  await goHome(page);
  const prompt = "Draft a short calculator test checklist. Do not write files.";
  await ideaInput(page).fill(prompt);
  await page.getByRole("button", { name: "Start", exact: true }).click();
  await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible({ timeout: 60_000 });
  await expect(liveChatStage(page).getByTestId("transcript-article-user")).toContainText(prompt);
});

webE2e("Enter shows the idea in its chat before the engine answers, then sends it once", async ({ page, request }) => {
  await bootstrapChatSession(page, request);
  await goHome(page);
  const prompts = countPrompts(page);
  const reserve = await hold(page, "**/v1/home-starts/*", "PUT");
  await ideaInput(page).fill(IDEA);
  const submitted = Date.now();
  await ideaInput(page).press("Enter");

  // The reservation is held unanswered; the chat is already foreground.
  const launch = page.getByTestId("launch-conversation");
  await expect(launch.getByTestId("pending-send-bubble")).toContainText(IDEA);
  const firstRender = Date.now() - submitted;
  await expect(page.getByTestId("home-idea-input")).toBeHidden();
  await expect(launch).toBeFocused();

  reserve.release();
  await expect(liveChatStage(page).getByTestId("transcript-article-user")).toContainText(IDEA, { timeout: 60_000 });
  expect([...prompts().values()]).toEqual([1]);
  test.info().annotations.push({ type: "home-first-render-ms", description: String(firstRender) });
});

webE2e("a dropped reservation reply recovers the same chat and one first turn", async ({ page, request }) => {
  await bootstrapChatSession(page, request);
  await goHome(page);
  const prompts = countPrompts(page);
  let dropped = 0;
  await page.route("**/v1/home-starts/*", async (route: Route) => {
    if (route.request().method() !== "PUT" || dropped > 0) return route.fallback();
    dropped++;
    // The host commits the reservation; only its reply is lost.
    await route.fetch();
    await route.abort("failed");
  });
  await ideaInput(page).fill(IDEA);
  await ideaInput(page).press("Enter");
  await expect(page.getByTestId("launch-conversation").getByTestId("pending-send-bubble")).toContainText(IDEA);
  await expect(liveChatStage(page).getByTestId("transcript-article-user")).toContainText(IDEA, { timeout: 60_000 });
  expect(dropped).toBe(1);
  expect([...prompts().values()]).toEqual([1]);
  await expect(liveChatStage(page).getByTestId("transcript-article-user")).toHaveCount(1);
});

webE2e("an attachment-only idea opens its chat and sends the file", async ({ page, request }) => {
  await bootstrapChatSession(page, request);
  await goHome(page);
  await ideaInput(page).evaluate((element) => {
    const data = new DataTransfer();
    data.items.add(new File(["calculator notes"], "notes.txt", { type: "text/plain" }));
    element.dispatchEvent(new ClipboardEvent("paste", { clipboardData: data, bubbles: true, cancelable: true }));
  });
  await expect(page.getByTestId("home-idea-attachment-chip")).toHaveCount(1);
  await page.getByRole("button", { name: "Start", exact: true }).click();
  await expect(page.getByTestId("launch-conversation").getByTestId("pending-send-attachment-chips")).toContainText("notes.txt");
  await expect(liveChatStage(page).getByTestId("transcript-article-user")).toBeVisible({ timeout: 60_000 });
});

webE2e("Stop before the engine answers sends nothing and keeps the idea", async ({ page, request }) => {
  await bootstrapChatSession(page, request);
  await goHome(page);
  const prompts = countPrompts(page);
  const reserve = await hold(page, "**/v1/home-starts/*", "PUT");
  await ideaInput(page).fill(IDEA);
  await ideaInput(page).press("Enter");
  const launch = page.getByTestId("launch-conversation");
  await launch.getByTestId("launch-stop").click();
  reserve.release();
  await expect(launch.getByTestId("launch-notice")).toContainText("Stopped");
  await expect(launch.getByTestId("pending-send-bubble")).toContainText(IDEA);
  expect(prompts().size).toBe(0);
  await launch.getByTestId("launch-action-edit_on_home").click();
  await expect(page.getByTestId("home-idea-input")).toHaveValue(IDEA);
});
