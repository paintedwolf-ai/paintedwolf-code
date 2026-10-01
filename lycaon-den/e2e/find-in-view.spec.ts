import { expect, test } from "@playwright/test";
import {
  activateProject,
  apiFindHarnessProject,
  apiPostPrompt,
  liveChatStage,
  openNewChatSession,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";

const NEEDLE = "FIND_HARNESS_NEEDLE_42";

type Harness = {
  llm: {
    manual: () => Promise<unknown>;
    pending: (ms?: number) => Promise<unknown>;
    respond: (body: { text: string }) => Promise<unknown>;
  };
};

/**
 * In-view find on the chat transcript via Mod+F.
 * den:harness:test defaults LYCAON_LLM_MANUAL=1; seed via API + scripted reply.
 */
webE2e("den:harness — transcript in-view find next prev escape", async ({ page, request }) => {
  test.skip(
    process.env.LYCAON_LLM_MANUAL !== "1",
    "harness find smoke expects LYCAON_LLM_MANUAL=1 (den:harness:test default)",
  );
  test.setTimeout(180_000);

  await page.goto("/");
  await waitHarnessConnected(page);

  const project = await apiFindHarnessProject(request);
  await activateProject(page, project.id);
  const sessionId = await openNewChatSession(page);

  await page.evaluate(async () => {
    const h = (window as unknown as { __harness: Harness }).__harness;
    await h.llm.manual();
  });

  const promptRes = await apiPostPrompt(request, sessionId, {
    text: `please remember ${NEEDLE} in this chat`,
  });
  expect(promptRes.ok(), await promptRes.text()).toBeTruthy();

  await page.evaluate(async (needle) => {
    const h = (window as unknown as { __harness: Harness }).__harness;
    await h.llm.pending(60_000);
    await h.llm.respond({ text: `ack ${needle}` });
  }, NEEDLE);

  await expect(page.getByText(NEEDLE).first()).toBeVisible({ timeout: 60_000 });

  await liveChatStage(page).getByTestId("chat-stream").click({ timeout: 15_000 });
  await page.keyboard.press("ControlOrMeta+f");

  const findInput = page.getByTestId("find-bar-input");
  await expect(findInput).toBeVisible({ timeout: 5_000 });
  // Chat-scoped Find docks above the composer, not the Shell top host.
  await expect(page.getByTestId("find-bar-host-composer")).toBeVisible();
  await expect(page.getByTestId("find-bar-host")).toHaveCount(0);
  await findInput.fill(NEEDLE);
  await expect(page.getByTestId("find-bar-count")).toContainText(/of/, {
    timeout: 5_000,
  });
  await expect(page.locator('span[data-den-find-mark="1"]').first()).toBeVisible({
    timeout: 5_000,
  });

  const dock = liveChatStage(page).getByTestId("composer-chrome-stack");
  await expect(dock).toBeVisible();
  await expect(dock.getByTestId("find-bar-host-composer")).toBeVisible();

  await page.keyboard.press("ControlOrMeta+g");
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("find-bar")).toHaveCount(0);
  await expect(page.locator('span[data-den-find-mark="1"]')).toHaveCount(0);
});
