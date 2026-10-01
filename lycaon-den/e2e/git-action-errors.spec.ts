import { execFileSync } from "node:child_process";
import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import {
  activateProject,
  apiCreateProjectWithRoot,
  e2eTempDir,
  e2eUniqueLabel,
  gotoShell,
  modelIndependentWebE2e,
  openBottomTab,
  seedAppState,
  settledChatSessionId,
} from "./helpers.ts";

for (const operation of ["commit", "stash", "discard", "push", "pull"] as const) {
  modelIndependentWebE2e(`Git ${operation} failure remains visible and retryable by the user`, async ({ page, request }) => {
    const root = e2eTempDir(request, "git-action-error");
    const env = {
      ...process.env,
      GIT_CONFIG_GLOBAL: "/dev/null",
      GIT_CONFIG_SYSTEM: "/dev/null",
      GIT_AUTHOR_NAME: "Fixture",
      GIT_AUTHOR_EMAIL: "fixture@example.com",
      GIT_COMMITTER_NAME: "Fixture",
      GIT_COMMITTER_EMAIL: "fixture@example.com",
    };
    const git = (...args: string[]) => execFileSync("git", ["-C", root, ...args], { env, stdio: "pipe" });
    git("init", "-b", "main");
    writeFileSync(path.join(root, "README.md"), "initial\n");
    git("add", "README.md");
    git("commit", "-m", "initial");
    writeFileSync(path.join(root, "README.md"), "edited\n");
    const project = await apiCreateProjectWithRoot(request, root, e2eUniqueLabel("Git error"));
    // Supply sync state without contacting a real remote in automated tests.
    await page.route("**/v1/projects/*/git/repos/*/status**", async (route) => {
      const response = await route.fetch();
      await route.fulfill({ response, json: { ...await response.json(), upstream: "origin/main", ahead: 1, behind: 0 } });
    });
    let attempts = 0;
    await page.route(`**/v1/projects/*/git/repos/*/${operation}`, async (route) => {
      attempts++;
      await route.fulfill({ status: 422, json: {
        code: `git_${operation}_failed`,
        title: `Could not ${operation}`,
        message: `Fixture ${operation} refusal`,
        suggested_action: "Review the repository before retrying.",
        tier: "non_catastrophic", scope: "session",
      } });
    });
    await seedAppState(page);
    await gotoShell(page);
    await activateProject(page, project.id);
    await settledChatSessionId(page);
    await openBottomTab(page, "git");
    for (let tip = 0; tip < 6; tip++) {
      const dismiss = page.getByTestId("first-time-tip-dismiss");
      if (!(await dismiss.isVisible())) break;
      await dismiss.click();
      await expect(dismiss).toHaveCount(0);
    }
    if (operation === "commit") await page.getByTestId("git-message").fill("fixture commit");
    const control = page.getByTestId(`git-${operation}`);
    await expect(control).toBeEnabled();
    await control.click();
    if (operation === "discard") await page.getByTestId("git-discard-confirm").click();
    await expect(page.getByTestId("notice-rail")).toContainText(`Fixture ${operation} refusal`);
    await expect(page.getByTestId("notice-rail")).toContainText("Review the repository before retrying.");
    await expect(control).toBeEnabled();
    if (operation === "commit") await expect(page.getByTestId("git-message")).toHaveValue("fixture commit");
    expect(attempts).toBe(1);
    expect(execFileSync("git", ["-C", root, "diff", "--", "README.md"], { env, encoding: "utf8" })).toContain("+edited");
  });
}
