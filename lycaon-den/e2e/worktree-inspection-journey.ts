import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import {
  expect,
  type APIRequestContext,
  type BrowserContext,
  type Page,
  type TestInfo,
} from "@playwright/test";
import {
  activateProject,
  apiAttachProjectRoot,
  apiCreateProjectWithRoot,
  apiJson,
  e2eTempDir,
  e2eUniqueLabel,
  gotoShell,
  openBottomTab,
  seedAppState,
  settledChatSessionId,
} from "./helpers.ts";

type GitWorktreeView = {
  bound: boolean;
  session_id: string;
  worktree_id?: string;
  branch?: string;
  base_branch?: string;
  state?: string;
};

function gitEnv(): NodeJS.ProcessEnv {
  return {
    ...process.env,
    GIT_CONFIG_GLOBAL: "/dev/null",
    GIT_CONFIG_SYSTEM: "/dev/null",
    GIT_AUTHOR_NAME: "Qualification",
    GIT_AUTHOR_EMAIL: "qual@example.test",
    GIT_COMMITTER_NAME: "Qualification",
    GIT_COMMITTER_EMAIL: "qual@example.test",
  };
}

function runGit(cwd: string, ...args: string[]): string {
  return execFileSync("git", ["-C", cwd, ...args], {
    env: gitEnv(),
    encoding: "utf8",
  }).trim();
}

function worktreePathForBranch(toplevel: string, branch: string): string {
  const out = runGit(toplevel, "worktree", "list", "--porcelain");
  for (const block of out.split(/\n\n+/)) {
    const lines = block.split("\n").filter(Boolean);
    const wtLine = lines.find((l) => l.startsWith("worktree "));
    const brLine = lines.find((l) => l.startsWith("branch "));
    if (!wtLine || !brLine) continue;
    const ref = brLine.slice("branch ".length).trim();
    if (ref === `refs/heads/${branch}` || ref === branch) {
      return wtLine.slice("worktree ".length).trim();
    }
  }
  throw new Error(`missing worktree for branch ${branch}:\n${out}`);
}

async function dismissTips(page: Page) {
  for (let i = 0; i < 6; i += 1) {
    const dismiss = page.getByTestId("first-time-tip-dismiss");
    if (!(await dismiss.isVisible().catch(() => false))) return;
    await dismiss.click();
    await expect(dismiss).toHaveCount(0, { timeout: 5_000 }).catch(() => undefined);
  }
}

async function openGitPanel(page: Page) {
  await openBottomTab(page, "git");
  await expect(page.getByTestId("git-strip")).toBeVisible({ timeout: 30_000 });
  await dismissTips(page);
}

async function setBranchMenuOpen(page: Page, open: boolean) {
  const trigger = page.getByTestId("git-branch");
  await expect(trigger).toBeVisible({ timeout: 20_000 });
  if (((await trigger.getAttribute("aria-expanded")) === "true") !== open) {
    await trigger.click();
  }
}

export async function runWorktreeInspectionQualificationJourney(args: {
  page: Page;
  context: BrowserContext;
  request: APIRequestContext;
  testInfo: TestInfo;
}): Promise<void> {
  const { page, context, request, testInfo } = args;
  await page.setViewportSize({ width: 1600, height: 1000 });

  const baseDir = e2eTempDir(request, "wt-qual");
  const repoAlpha = path.join(baseDir, "repo-alpha");
  const repoBeta = path.join(baseDir, "repo-beta");
  const slotA = path.join(baseDir, "slot-a");
  const slotB = path.join(baseDir, "slot-b");
  mkdirSync(path.join(repoAlpha, "src"), { recursive: true });
  mkdirSync(path.join(repoBeta, "src"), { recursive: true });
  mkdirSync(slotA, { recursive: true });
  mkdirSync(slotB, { recursive: true });

  runGit(repoAlpha, "init", "-b", "main");
  writeFileSync(path.join(repoAlpha, "src", "shared.ts"), "export const checkout = 'alpha-primary';\n");
  runGit(repoAlpha, "add", "-A");
  runGit(repoAlpha, "commit", "-m", "init alpha");

  runGit(repoBeta, "init", "-b", "main");
  writeFileSync(path.join(repoBeta, "src", "beta.ts"), "export const beta = 'beta-primary';\n");
  runGit(repoBeta, "add", "-A");
  runGit(repoBeta, "commit", "-m", "init beta");

  // Two sibling checkouts with identical leaf directory name ("wt") and modified src/shared.ts.
  const workerOneDir = path.join(slotA, "wt");
  const workerTwoDir = path.join(slotB, "wt");
  const staleExternalDir = path.join(baseDir, "wt-stale-external");
  runGit(repoAlpha, "worktree", "add", "-b", "feat/worker-one", workerOneDir, "HEAD");
  runGit(repoAlpha, "worktree", "add", "-b", "feat/worker-two", workerTwoDir, "HEAD");
  runGit(repoAlpha, "worktree", "add", "-b", "feat/stale-external", staleExternalDir, "HEAD");
  rmSync(staleExternalDir, { recursive: true, force: true });

  const commonDir = path.resolve(repoAlpha, runGit(repoAlpha, "rev-parse", "--git-common-dir"));
  const staleGitdirFile = path.join(commonDir, "worktrees", "wt-stale-external", "gitdir");
  expect(existsSync(staleGitdirFile)).toBe(true);

  writeFileSync(path.join(workerOneDir, "src", "shared.ts"), "export const checkout = 'worker-one-edit';\n");
  writeFileSync(path.join(workerTwoDir, "src", "shared.ts"), "export const checkout = 'worker-two-edit';\n");

  const created = await apiCreateProjectWithRoot(request, repoAlpha, e2eUniqueLabel("Worktree qual"));
  await apiAttachProjectRoot(request, created.id, repoBeta, "beta");

  await seedAppState(page);
  await gotoShell(page);
  await activateProject(page, created.id);
  await dismissTips(page);
  const sessionId = await settledChatSessionId(page);

  await openGitPanel(page);
  // Select repo-alpha explicitly when multi-repo project starts on All repositories.
  const scopeTrigger = page.getByTestId("git-scope");
  await expect(scopeTrigger).toBeVisible({ timeout: 20_000 });
  await scopeTrigger.click();
  await page.getByTestId("git-scope-item").filter({ hasText: "repo-alpha" }).first().click();
  await expect(scopeTrigger).toContainText("repo-alpha");

  // Open Branch and checkout in Window 1: verify identical basename checkouts disambiguate and pin worker-one.
  await setBranchMenuOpen(page, true);
  const selectorWin1 = page.getByTestId("git-worktree-selector");
  await expect(selectorWin1).toBeVisible({ timeout: 20_000 });
  await expect(selectorWin1).toContainText("slot-a/wt");
  await expect(selectorWin1).toContainText("slot-b/wt");
  const optionsWin1 = page.getByTestId("git-worktree-option");
  await expect
    .poll(async () => optionsWin1.count(), { timeout: 20_000 })
    .toBeGreaterThanOrEqual(3);

  await optionsWin1.filter({ hasText: "feat/worker-one" }).first().click();
  await expect(page.getByTestId("git-branch")).toContainText("feat/worker-one");
  await setBranchMenuOpen(page, false);
  await expect(page.getByTestId("git-count")).toContainText("1", { timeout: 20_000 });
  if ((await page.getByTestId("git-count").getAttribute("aria-expanded")) !== "true") {
    await page.getByTestId("git-count").click();
  }
  await expect(page.getByTestId("git-files")).toContainText("shared.ts", { timeout: 20_000 });

  // Open Window 2 on the same project and select worker-two independently without disturbing Window 1.
  const secondWindow = await context.newPage();
  try {
    await secondWindow.setViewportSize({ width: 1600, height: 1000 });
    await seedAppState(secondWindow);
    await gotoShell(secondWindow);
    await activateProject(secondWindow, created.id);
    await dismissTips(secondWindow);
    await openGitPanel(secondWindow);

    const scopeTrigger2 = secondWindow.getByTestId("git-scope");
    await expect(scopeTrigger2).toBeVisible({ timeout: 20_000 });
    await scopeTrigger2.click();
    await secondWindow.getByTestId("git-scope-item").filter({ hasText: "repo-alpha" }).first().click();
    await expect(scopeTrigger2).toContainText("repo-alpha");

    // Window 2 starts following primary ("main"), proving Window 1's pin did not leak across windows.
    await expect(secondWindow.getByTestId("git-branch")).toContainText("main");
    await setBranchMenuOpen(secondWindow, true);
    await secondWindow.getByTestId("git-worktree-option").filter({ hasText: "feat/worker-two" }).first().click();
    await expect(secondWindow.getByTestId("git-branch")).toContainText("feat/worker-two");
    await setBranchMenuOpen(secondWindow, false);
    await expect(secondWindow.getByTestId("git-checkout-directory")).toContainText("slot-b/wt");

    // Window 1 remains pinned to feat/worker-one (slot-a/wt).
    await expect(page.getByTestId("git-branch")).toContainText("feat/worker-one");
    await expect(page.getByTestId("git-checkout-directory")).toContainText("slot-a/wt");

    // Switch Window 1 to repo-beta and back: per-repository pin in repo-alpha is preserved.
    await scopeTrigger.click();
    await page.getByTestId("git-scope-item").filter({ hasText: "beta" }).first().click();
    await expect(scopeTrigger).toContainText("beta");
    await expect(page.getByTestId("git-branch")).toContainText("main");
    await scopeTrigger.click();
    await page.getByTestId("git-scope-item").filter({ hasText: "repo-alpha" }).first().click();
    await expect(scopeTrigger).toContainText("repo-alpha");
    await expect(page.getByTestId("git-branch")).toContainText("feat/worker-one");

    // Bind a managed chat worktree while inspecting feat/worker-one and verify removal blockers.
    await setBranchMenuOpen(page, true);
    await page.getByTestId("git-worktree-unpin").click();
    await expect(page.getByTestId("git-branch")).toContainText("main");

    await setBranchMenuOpen(page, true);
    await page.getByTestId("git-worktree-bind-open").click();
    await page.getByTestId("git-worktree-branch").fill("session/e2e-managed");
    await page.getByTestId("git-worktree-create").click();
    await expect(page.getByTestId("git-checkout-create-modal")).toHaveCount(0, { timeout: 30_000 });

    await expect
      .poll(
        async () => {
          const view = await apiJson<GitWorktreeView>(
            request,
            "GET",
            `/v1/sessions/${encodeURIComponent(sessionId)}/git-worktree`,
          );
          return view.bound && view.state === "ready" && Boolean(view.worktree_id);
        },
        { timeout: 30_000 },
      )
      .toBe(true);
    const managedPath = worktreePathForBranch(repoAlpha, "session/e2e-managed");

    // Pin inspection to feat/worker-one while session/e2e-managed is bound and dirty.
    await setBranchMenuOpen(page, true);
    await page.getByTestId("git-worktree-option").filter({ hasText: "feat/worker-one" }).first().click();
    await expect(page.getByTestId("git-branch")).toContainText("feat/worker-one");
    await setBranchMenuOpen(page, false);

    writeFileSync(path.join(managedPath, "dirty-blocker.txt"), "uncommitted blocker\n");
    await expect(page.getByTestId("git-worktree-unbind")).toBeVisible({ timeout: 20_000 });
    await page.getByTestId("git-worktree-unbind").click();
    const unbindModal = page.getByTestId("git-worktree-unbind-modal");
    await expect(unbindModal).toBeVisible();
    await expect(unbindModal).toContainText("session/e2e-managed");
    await page.getByTestId("git-worktree-unbind-confirm").click();
    await expect(unbindModal.getByRole("alert")).toBeVisible({ timeout: 15_000 });
    await expect(page.getByTestId("git-worktree-blocker-action")).toBeVisible();

    // Clean the managed worktree blocker and confirm removal; verify worker-one and unrelated stale registration survive.
    rmSync(path.join(managedPath, "dirty-blocker.txt"), { force: true });
    await page.getByTestId("git-worktree-unbind-confirm").click();
    await expect(unbindModal).toHaveCount(0, { timeout: 30_000 });
    expect(existsSync(managedPath)).toBe(false);
    expect(existsSync(workerOneDir)).toBe(true);
    expect(existsSync(staleGitdirFile)).toBe(true);

    await page.screenshot({ path: testInfo.outputPath("worktree-qualification-win1.png") });
    await secondWindow.screenshot({ path: testInfo.outputPath("worktree-qualification-win2.png") });
  } finally {
    await secondWindow.close();
  }
}
