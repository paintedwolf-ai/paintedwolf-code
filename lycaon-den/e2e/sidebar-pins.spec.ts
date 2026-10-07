import { expect, type APIRequestContext, type Page } from "@playwright/test";
import {
  apiJson,
  bootstrapChatSession,
  webE2e,
} from "./helpers.ts";

/**
 * The project rail's chat list: a Pinned group the person arranges, chats in
 * a chosen order, and rows that do not move because a chat was read. Opening
 * a project starts an empty chat; seeding waits until the host has created it,
 * so it is the oldest, untitled row.
 */

const UNTITLED = "Untitled chat";

type CreatedSession = { id: string };
type ListPage = { sessions: Array<{ id: string; title?: string }> };
type SeenSession = { seen_at?: string };

async function seedChat(request: APIRequestContext, projectId: string, title: string): Promise<string> {
  const session = await apiJson<CreatedSession>(request, "POST", "/v1/sessions", {
    project_id: projectId,
    posture: "build",
  });
  await apiJson(request, "PATCH", `/v1/sessions/${session.id}`, { title });
  return session.id;
}

async function groupTitles(page: Page, group: "pinned" | "chats"): Promise<string[]> {
  const list = page.getByTestId(`focused-session-list-${group}`);
  if ((await list.count()) === 0) return [];
  return list.locator(".focused-session-list__title").allTextContents();
}

async function hostPinnedTitles(request: APIRequestContext, projectId: string): Promise<string[]> {
  const page = await apiJson<ListPage>(
    request,
    "GET",
    `/v1/projects/${projectId}/sessions?pinned=true&sort=pin`,
  );
  return page.sessions.map((s) => s.title ?? "");
}

function row(page: Page, title: string) {
  return page
    .getByTestId("focused-session-list")
    .locator(".den-list-row--nav", { has: page.locator(".focused-session-list__title", { hasText: title }) });
}

/** Park the pointer outside the list so held order is released. */
async function leaveList(page: Page) {
  await page.mouse.move(900, 400);
}

webE2e("pins chats, arranges the pinned group, and keeps the order", async ({ page, request }) => {
  const project = await bootstrapChatSession(page, request);
  await seedChat(request, project.id, "Alpha plan");
  await seedChat(request, project.id, "Beta fix");
  await seedChat(request, project.id, "Gamma audit");
  await expect.poll(() => groupTitles(page, "chats"), { timeout: 30_000 })
    .toEqual(["Gamma audit", "Beta fix", "Alpha plan", UNTITLED]);

  await row(page, "Alpha plan").hover();
  await row(page, "Alpha plan").getByTestId("session-pin-action").click();
  await expect.poll(() => groupTitles(page, "pinned")).toEqual(["Alpha plan"]);

  await row(page, "Gamma audit").click({ button: "right" });
  await page.getByTestId("session-menu-pin").click();
  await expect.poll(() => groupTitles(page, "pinned")).toEqual(["Alpha plan", "Gamma audit"]);
  await expect.poll(() => groupTitles(page, "chats")).toEqual(["Beta fix", UNTITLED]);
  await expect.poll(() => hostPinnedTitles(request, project.id)).toEqual(["Alpha plan", "Gamma audit"]);

  // Drag the second pin above the first.
  const second = row(page, "Gamma audit");
  const first = row(page, "Alpha plan");
  const from = await second.boundingBox();
  const to = await first.boundingBox();
  if (!from || !to) throw new Error("pinned rows have no boxes");
  await page.mouse.move(from.x + 40, from.y + from.height / 2);
  await page.mouse.down();
  await page.mouse.move(from.x + 40, from.y + from.height / 2 - 6, { steps: 3 });
  await page.mouse.move(from.x + 40, to.y + to.height / 2, { steps: 6 });
  await page.mouse.up();
  await expect.poll(() => groupTitles(page, "pinned")).toEqual(["Gamma audit", "Alpha plan"]);
  await expect.poll(() => hostPinnedTitles(request, project.id)).toEqual(["Gamma audit", "Alpha plan"]);

  // The keyboard-reachable path: Move down from the row's menu.
  await row(page, "Gamma audit").click({ button: "right" });
  await page.getByTestId("session-menu-move-down").click();
  await expect.poll(() => hostPinnedTitles(request, project.id)).toEqual(["Alpha plan", "Gamma audit"]);
  await leaveList(page);
  await expect.poll(() => groupTitles(page, "pinned")).toEqual(["Alpha plan", "Gamma audit"]);

  await page.reload();
  await expect.poll(() => groupTitles(page, "pinned"), { timeout: 30_000 })
    .toEqual(["Alpha plan", "Gamma audit"]);
});

webE2e("reading a chat leaves it where it is", async ({ page, request }) => {
  const project = await bootstrapChatSession(page, request);
  const oldest = await seedChat(request, project.id, "Oldest");
  await seedChat(request, project.id, "Middle");
  await seedChat(request, project.id, "Newest");
  await expect.poll(() => groupTitles(page, "chats"), { timeout: 30_000 })
    .toEqual(["Newest", "Middle", "Oldest", UNTITLED]);

  await row(page, "Oldest").click();
  await leaveList(page);
  // Opening marks the chat read on the host; the order stays put.
  await expect.poll(async () => (await apiJson<SeenSession>(request, "GET", `/v1/sessions/${oldest}`)).seen_at ?? "")
    .not.toBe("");
  expect(await groupTitles(page, "chats")).toEqual(["Newest", "Middle", "Oldest", UNTITLED]);
});

webE2e("orders chats from the menu beside the heading, on every project", async ({ page, request }) => {
  const project = await bootstrapChatSession(page, request);
  await seedChat(request, project.id, "beta");
  await seedChat(request, project.id, "Charlie");
  await seedChat(request, project.id, "alpha");
  await expect.poll(() => groupTitles(page, "chats"), { timeout: 30_000 })
    .toEqual(["alpha", "Charlie", "beta", UNTITLED]);

  await page.getByTestId("focused-session-list-sort").click();
  await page.getByTestId("focused-session-list-sort-title").click();
  await leaveList(page);
  // An empty title sorts first, as the host pages it.
  await expect.poll(() => groupTitles(page, "chats")).toEqual([UNTITLED, "alpha", "beta", "Charlie"]);
  await expect(page.getByTestId("focused-session-list-sort")).toHaveText(/Title/);

  await page.reload();
  await expect.poll(() => groupTitles(page, "chats"), { timeout: 30_000 })
    .toEqual([UNTITLED, "alpha", "beta", "Charlie"]);
});
