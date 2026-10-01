import { expect } from "@playwright/test";
import {
  apiGetActiveWorkflowRun,
  captureWireSessionId,
  openWorkflowsTab,
  webE2e,
} from "./helpers.ts";

async function harnessReady(page: import("@playwright/test").Page) {
  await page.goto("/");
  await page.waitForFunction(
    () =>
      (window as unknown as { __harness?: { state(): { connected: boolean } } }).__harness?.state()
        .connected === true,
    undefined,
    { timeout: 60_000 },
  );
}

async function startCatalogWorkflow(page: import("@playwright/test").Page, trigger: string) {
  await page.evaluate(async (cmd) => {
    const h = (
      window as unknown as {
        __harness: {
          openProject: (name: string) => Promise<unknown>;
          newSession: () => Promise<unknown>;
          sendPrompt: (text: string) => Promise<{ ok: boolean }>;
        };
      }
    ).__harness;
    await h.openProject("Harness");
    await h.newSession();
    const sent = await h.sendPrompt(`${cmd} Run this workflow against the current project.`);
    if (!sent.ok) throw new Error("workflow prompt could not be sent");
  }, trigger);
}

const demoTriggers: { trigger: string; workflowId: string }[] = [
  { trigger: "/security-survey", workflowId: "security-survey" },
  { trigger: "/recon", workflowId: "recon-pack" },
  { trigger: "/bugbash", workflowId: "bugbash" },
  { trigger: "/options", workflowId: "options" },
];

webE2e.describe("demo workflow smoke", () => {
  for (const spec of demoTriggers) {
    webE2e(`${spec.trigger} starts catalog run from harness`, async ({ page, request }) => {
      const sessions = captureWireSessionId(page);
      await harnessReady(page);
      await startCatalogWorkflow(page, spec.trigger);

      await expect.poll(() => sessions.get(), { timeout: 120_000 }).toBeTruthy();
      await expect.poll(async () => (await apiGetActiveWorkflowRun(request, sessions.get()!)).workflow_id, { timeout: 30_000 }).toBe(spec.workflowId);

      await openWorkflowsTab(page);
      await expect(page.getByTestId("workflow-active-row")).toBeVisible({ timeout: 30_000 });
      await expect(page.getByTestId("den-chat-span-run")).toHaveCount(1, { timeout: 30_000 });
    });
  }
});
