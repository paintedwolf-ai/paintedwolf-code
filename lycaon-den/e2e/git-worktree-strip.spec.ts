import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import {
  expect,
  test,
  type APIRequestContext,
  type Page,
} from "@playwright/test";
import {
  activateProject,
  apiCreateProjectWithRoot,
  apiJson,
  e2eUniqueLabel,
  e2eTempDir,
  gotoShell,
  liveChatStage,
  openBottomTab,
  seedAppState,
  settledChatSessionId,
  webE2e,
} from "./helpers.ts";

type GitWorktreeView = {
  bound: boolean;
  session_id: string;
  path?: string;
  branch?: string;
  base_branch?: string;
  state?: string;
  ahead_of_base: number;
};

function gitEnv(): NodeJS.ProcessEnv {
  return {
    ...process.env,
    GIT_CONFIG_GLOBAL: "/dev/null",
    GIT_CONFIG_SYSTEM: "/dev/null",
    GIT_AUTHOR_NAME: "Harness",
    GIT_AUTHOR_EMAIL: "harness@example.com",
    GIT_COMMITTER_NAME: "Harness",
    GIT_COMMITTER_EMAIL: "harness@example.com",
  };
}

function runGit(cwd: string, ...args: string[]) {
  execFileSync("git", ["-C", cwd, ...args], {
    env: gitEnv(),
    stdio: "pipe",
  });
}

/** Ready views omit path; resolve the linked checkout from git itself. */
function worktreePathForBranch(toplevel: string, branch: string): string {
  const out = execFileSync(
    "git",
    ["-C", toplevel, "worktree", "list", "--porcelain"],
    { env: gitEnv(), encoding: "utf8" },
  );
  const blocks = out.split(/\n\n+/);
  for (const block of blocks) {
    const lines = block.split("\n").filter(Boolean);
    const wtLine = lines.find((l) => l.startsWith("worktree "));
    const brLine = lines.find((l) => l.startsWith("branch "));
    if (!wtLine || !brLine) continue;
    const ref = brLine.slice("branch ".length).trim();
    if (ref === `refs/heads/${branch}` || ref === branch) {
      return wtLine.slice("worktree ".length).trim();
    }
  }
  throw new Error(`no worktree for branch ${branch}\n${out}`);
}

function initCommittedRepo(request: APIRequestContext): string {
  const dir = e2eTempDir(request, "git-worktree");
  runGit(dir, "init", "-b", "main");
  runGit(dir, "config", "user.email", "harness@example.com");
  runGit(dir, "config", "user.name", "Harness");
  writeFileSync(path.join(dir, "README.md"), "hello\n");
  runGit(dir, "add", "README.md");
  runGit(dir, "commit", "-m", "init");
  return dir;
}

async function dismissFirstTimeTips(page: Page) {
  for (let i = 0; i < 6; i += 1) {
    const dismiss = page.getByTestId("first-time-tip-dismiss");
    if (!(await dismiss.isVisible().catch(() => false))) return;
    await dismiss.click();
    await expect(dismiss)
      .toHaveCount(0, { timeout: 5_000 })
      .catch(() => undefined);
  }
}

async function openGitTab(page: Page) {
  await openBottomTab(page, "git");
  await expect(page.getByTestId("git-strip")).toBeVisible({ timeout: 30_000 });
  await dismissFirstTimeTips(page);
}

/** Close the Git panel so GitStrip remounts and reloads the worktree signal. */
async function remountGitTab(page: Page) {
  if (
    await page
      .getByTestId("git-strip")
      .isVisible()
      .catch(() => false)
  ) {
    await page.getByTestId("tab-git").click();
    await expect(page.getByTestId("git-strip")).toHaveCount(0, {
      timeout: 15_000,
    });
  }
  await openGitTab(page);
}

async function commitThroughGitTab(page: Page, message: string) {
  await remountGitTab(page);
  const input = page.getByTestId("git-message");
  await expect(input).toBeVisible({ timeout: 30_000 });
  await input.fill(message);
  await page.getByTestId("git-commit").click();
  await expect(input).toHaveCount(0, { timeout: 30_000 });
}

async function waitForAhead(
  request: APIRequestContext,
  sessionId: string,
  min = 1,
): Promise<GitWorktreeView> {
  let last: GitWorktreeView | undefined;
  await expect
    .poll(
      async () => {
        last = await fetchWorktree(request, sessionId);
        return last.ahead_of_base;
      },
      { timeout: 30_000 },
    )
    .toBeGreaterThanOrEqual(min);
  return last!;
}

async function fetchWorktree(
  request: APIRequestContext,
  sessionId: string,
): Promise<GitWorktreeView> {
  return apiJson<GitWorktreeView>(request, "GET", `/v1/sessions/${encodeURIComponent(sessionId)}/git-worktree`);
}

webE2e("mounted Files follows branch and worktree checkout changes", async ({ page, request }) => {
  await page.setViewportSize({ width: 1600, height: 1000 });
  const root = initCommittedRepo(request);
  const project = await apiCreateProjectWithRoot(request, root, e2eUniqueLabel("Checkout refresh"));
  await seedAppState(page);
  await gotoShell(page);
  await activateProject(page, project.id);
  await dismissFirstTimeTips(page);
  const sessionId = await settledChatSessionId(page);
  await page.getByTestId("project-files-entry").click();
  await page.getByTestId("layout-dock-btn").click();
  await page.getByTestId("layout-tile-split").click();
  await page.getByTestId("layout-dock-btn").click();
  const summaryPath = page.getByTestId("files-root-summary-path");
  const summaryBranch = page.getByTestId("files-root-summary-branch");
  await expect(summaryPath).toHaveText(root);
  await openGitTab(page);
  await page.getByTestId("git-branch").click();
  await page.getByTestId("git-new-branch-open").click();
  await page.getByLabel("New branch name").fill("chore/folder-refresh");
  await page.getByLabel("New branch name").press("Enter");
  await expect(summaryBranch).toContainText("chore/folder-refresh");
  await page.getByTestId("git-branch").click();
  await page.getByTestId("git-worktree-bind-open").click();
  await page.getByLabel("New branch name").fill("chore/worktree-refresh");
  await page.getByTestId("git-worktree-create").click();
  await expect(page.getByTestId("git-checkout-location")).toContainText("Chat worktree");
  const worktree = await fetchWorktree(request, sessionId);
  const worktreePath = worktreePathForBranch(root, worktree.branch!);
  await expect(summaryPath).toHaveText(worktreePath);
  await expect(summaryBranch).toContainText("chore/worktree-refresh");
  await page.getByTestId("git-worktree-unbind").click();
  await page.getByTestId("git-worktree-unbind-confirm").click();
  await expect(summaryPath).toHaveText(root);
  await expect(summaryBranch).toContainText("chore/folder-refresh");
  expect(existsSync(worktreePath)).toBe(false);
});

webE2e(
  "git checkout flow: branches, worktree isolation, merge and recovery",
  async ({ page, request }) => {
    test.setTimeout(180_000);
    const root = initCommittedRepo(request);
    const project = await apiCreateProjectWithRoot(
      request,
      root,
      e2eUniqueLabel("Worktree"),
    );
    await seedAppState(page);
    await gotoShell(page);
    await activateProject(page, project.id);
    await dismissFirstTimeTips(page);
    const sessionId = await settledChatSessionId(page);
    await expect(liveChatStage(page).getByTestId("chat-composer")).toBeVisible({
      timeout: 90_000,
    });

    await openGitTab(page);
    await expect(page.getByTestId("git-checkout-location")).toContainText(
      "Project folder",
    );
    await expect(page.getByTestId("git-worktree-bind-open")).toHaveCount(0);
    writeFileSync(path.join(root, "draft.txt"), "unfinished project work\n");
    await remountGitTab(page);
    await expect(page.getByTestId("git-count")).toContainText("1 changed");

    await page.getByTestId("git-branch").click();
    await page.getByTestId("git-new-branch-open").click();
    await expect(page.getByLabel("New branch name")).toBeFocused();
    await page.getByLabel("New branch name").fill("feature/project-folder");
    await page.getByLabel("New branch name").press("Enter");
    await expect(page.getByTestId("git-branch")).toContainText(
      "feature/project-folder",
    );
    await expect(page.getByTestId("git-checkout-location")).toContainText(
      "Project folder",
    );
    expect(readFileSync(path.join(root, "draft.txt"), "utf8")).toBe(
      "unfinished project work\n",
    );
    await page.getByTestId("git-branch").click();
    await page
      .getByTestId("git-branch-item")
      .filter({ hasText: "main" })
      .click();
    await expect(page.getByTestId("git-branch")).toContainText("main");

    await page.getByTestId("git-branch").click();
    await expect(page.getByTestId("git-worktree-bind-open")).toBeVisible();
    await page.getByTestId("git-worktree-bind-open").click();
    await expect(page.getByTestId("git-checkout-create-modal")).toBeVisible();
    await expect(page.getByTestId("git-checkout-create-modal")).toContainText(
      "1 changed file stays in the project folder and is not copied.",
    );
    await expect(page.getByTestId("git-worktree-create")).toBeDisabled();
    await page.getByLabel("New branch name").press("Escape");
    await expect(page.getByTestId("git-checkout-create-modal")).toHaveCount(0);
    await expect(page.getByTestId("git-branch")).toBeFocused();

    await page.getByTestId("git-branch").click();
    await expect(page.getByTestId("git-worktree-bind-open")).toBeVisible();
    await page.getByTestId("git-worktree-bind-open").click();
    await expect(page.getByTestId("git-checkout-create-modal")).toBeVisible();
    await page.getByLabel("New branch name").fill("main");
    await page.getByTestId("git-worktree-create").click();
    await expect(
      page.getByTestId("git-checkout-create-modal").getByRole("alert"),
    ).toContainText("already exists");
    await expect(page.getByLabel("New branch name")).toHaveValue("main");
    await page.getByLabel("New branch name").fill("feature/chat-worktree");
    await page.getByTestId("git-worktree-create").click();
    await expect(page.getByTestId("git-checkout-location")).toContainText(
      "Chat worktree",
      { timeout: 30_000 },
    );
    await expect(page.getByTestId("git-branch")).toContainText(
      "feature/chat-worktree",
    );
    await expect(page.getByTestId("git-worktree-land")).toBeDisabled();
    await expect(page.getByTestId("git-clean")).toBeVisible();

    const wt = await fetchWorktree(request, sessionId);
    expect(wt.bound).toBe(true);
    expect(wt.branch).toBeTruthy();
    const wtPath = worktreePathForBranch(root, wt.branch!);
    expect(existsSync(path.join(wtPath, "draft.txt"))).toBe(false);
    expect(readFileSync(path.join(root, "draft.txt"), "utf8")).toBe(
      "unfinished project work\n",
    );
    runGit(root, "add", "draft.txt");
    runGit(root, "commit", "-m", "save project draft");

    writeFileSync(path.join(wtPath, "feature.txt"), "from worktree\n");
    await commitThroughGitTab(page, "feat: worktree change");
    await waitForAhead(request, sessionId, 1);
    await expect(page.getByTestId("git-worktree-land")).toBeEnabled({
      timeout: 30_000,
    });

    await page.getByTestId("git-worktree-land").click();
    await expect(page.getByTestId("git-worktree-land-modal")).toBeVisible();
    await page.getByTestId("git-worktree-land-cancel").click();
    await expect(page.getByTestId("git-worktree-land-modal")).toHaveCount(0);

    await page.getByTestId("git-worktree-unbind").click();
    await expect(page.getByTestId("git-worktree-unbind-modal")).toBeVisible();
    await page.getByTestId("git-worktree-unbind-cancel").click();
    await expect(page.getByTestId("git-worktree-unbind-modal")).toHaveCount(0);

    await page.getByTestId("git-worktree-land").click();
    await page.getByTestId("git-worktree-land-confirm").click();
    await expect(page.getByTestId("git-worktree-result")).toContainText(
      "Merged 1 commit into main.",
    );
    expect(readFileSync(path.join(root, "feature.txt"), "utf8")).toBe(
      "from worktree\n",
    );
    await expect(page.getByTestId("git-checkout-location")).toContainText(
      "Chat worktree",
    );

    writeFileSync(path.join(wtPath, "README.md"), "wt\n");
    await commitThroughGitTab(page, "feat: conflicting worktree change");
    writeFileSync(path.join(root, "README.md"), "base\n");
    runGit(root, "add", "README.md");
    runGit(root, "commit", "-m", "base");
    await waitForAhead(request, sessionId, 1);
    await remountGitTab(page);
    await expect(page.getByTestId("git-worktree-land")).toBeEnabled({
      timeout: 30_000,
    });
    await page.getByTestId("git-worktree-land").click();
    await page.getByTestId("git-worktree-land-confirm").click();
    await expect(page.getByTestId("git-worktree-result")).toContainText(
      "The merge conflicts and was not applied",
      { timeout: 30_000 },
    );
    await expect(page.getByTestId("git-worktree-result")).toContainText(
      "README.md",
    );

    rmSync(wtPath, { recursive: true, force: true });
    await remountGitTab(page);
    await expect(page.getByTestId("git-worktree")).toContainText(
      "This chat's worktree is missing",
      { timeout: 30_000 },
    );
    await expect(page.getByTestId("git-worktree-unbind")).toBeVisible();
    await expect(page.getByTestId("git-worktree-land")).toHaveCount(0);
    await page.getByTestId("git-worktree-unbind").click();
    await page.getByTestId("git-worktree-unbind-confirm").click();
    await expect(page.getByTestId("git-checkout-location")).toContainText(
      "Project folder",
    );
    await expect(page.getByTestId("git-branch")).toContainText("main");
    expect((await fetchWorktree(request, sessionId)).bound).toBe(
      false,
    );
    runGit(root, "show-ref", "--verify", `refs/heads/${wt.branch!}`);
  },
);
