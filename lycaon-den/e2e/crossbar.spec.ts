import { expect, test, type Page } from "@playwright/test";
import {
  activateProject,
  apiFindHarnessProject,
  bootstrapChatSession,
  expectShellReady,
  webE2e,
} from "./helpers.ts";

async function waitShell(page: Page) {
  await expectShellReady(page);
}

async function openCrossbar(page: Page) {
  await page.keyboard.press("ControlOrMeta+k");
  await expect(page.getByTestId("crossbar")).toBeVisible({ timeout: 10_000 });
  // Mode chords are handled by the panel's keydown; focus lands after open.
  await expect(page.getByTestId("crossbar-input")).toBeFocused();
}

/** Crossbar overlay and escalation into full search depth. */
webE2e.describe("Crossbar", () => {
  webE2e("Mod+K finds the settings action and closes the overlay", async ({
    page,
  }) => {
    test.setTimeout(120_000);
    await page.goto("/");
    await waitShell(page);

    await openCrossbar(page);
    await page.getByTestId("crossbar-input").fill("Open settings");
    const openSettings = page.getByRole("button", { name: /^Open settings/ });
    await expect(openSettings).toBeVisible();
    await openSettings.click();
    await expect(page.getByTestId("crossbar")).toHaveCount(0);
    await expect(page.getByTestId("settings-view")).toBeVisible({
      timeout: 15_000,
    });
  });

  webE2e("Mod+Shift+] cycles modes; escalate preserves query and depth chrome", async ({
    page,
    request,
  }) => {
    test.setTimeout(180_000);
    await page.goto("/");
    await waitShell(page);

    const project = await apiFindHarnessProject(request);
    await activateProject(page, project.id);

    await openCrossbar(page);
    const input = page.getByTestId("crossbar-input");
    await expect(
      page.getByTestId("crossbar-mode-everything"),
    ).toHaveAttribute("aria-selected", "true");

    await page.keyboard.press("ControlOrMeta+Shift+]");
    await expect(page.getByTestId("crossbar-mode-actions")).toHaveAttribute(
      "aria-selected",
      "true",
    );
    // Mod+Shift+] walks into the content groups: Messages is the first of them.
    await page.keyboard.press("ControlOrMeta+Shift+]");
    await expect(page.getByTestId("crossbar-mode-messages")).toHaveAttribute(
      "aria-selected",
      "true",
    );

    const needle = "preview fixture";
    await input.fill(needle);
    await expect(page.getByTestId("crossbar-escalate")).toBeVisible({
      timeout: 5_000,
    });
    await page.getByTestId("crossbar-escalate").click();

    const stage = page.getByTestId("global-search-view");
    await expect(stage).toBeVisible({ timeout: 15_000 });
    await expect(page.getByTestId("crossbar")).toHaveCount(0);
    await expect(page.getByTestId("search-query-input")).toHaveValue(
      `kind:message ${needle}`,
    );
    await expect(page.getByTestId("search-scope-control")).toBeVisible();
    await expect(page.getByTestId("search-query-bar")).toBeVisible();
  });

  webE2e("Esc dismisses the overlay; Esc dismisses full search stage", async ({
    page,
  }) => {
    test.setTimeout(120_000);
    await page.goto("/");
    await waitShell(page);

    await openCrossbar(page);
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("crossbar")).toHaveCount(0);
    await expect(page.getByTestId("global-search-view")).toHaveCount(0);

    await openCrossbar(page);
    await page.getByTestId("crossbar-input").fill("project:current");
    await page.getByTestId("crossbar-escalate").click();
    await expect(page.getByTestId("global-search-view")).toBeVisible({
      timeout: 15_000,
    });
    const searchSurface = page.locator('[data-resident-key$=":search"]');
    await page.keyboard.press("Escape");
    await expect(searchSurface).toHaveAttribute("data-resident", "idle");
    await expect(searchSurface).toHaveAttribute("aria-hidden", "true");
    await expect(searchSurface).toHaveAttribute("inert", "");
  });

  webE2e("go-to lane: idle lists recent chats; typing matches the project", async ({
    page,
  }) => {
    test.setTimeout(120_000);
    await bootstrapChatSession(page);

    await openCrossbar(page);
    // Idle home leads with the most recent chat (jump-back-in row).
    const chat = page.getByTestId("crossbar-goto").first();
    await expect(chat).toBeVisible();
    await expect(chat).toHaveAttribute("data-goto-kind", "session");

    // Typing the project name surfaces a project go-to row.
    await page.getByTestId("crossbar-input").fill("Harness");
    const project = page.locator(
      '[data-testid="crossbar-goto"][data-goto-kind="project"]',
    );
    await expect(project).toBeVisible();
    await project.click();
    await expect(page.getByTestId("crossbar")).toHaveCount(0);
    await expect(page.getByTestId("shell")).toBeVisible();
  });

  webE2e("filename query surfaces a File row that opens the file", async ({
    page,
  }) => {
    test.setTimeout(120_000);
    await bootstrapChatSession(page);

    await openCrossbar(page);
    // go.mod ships in the harness fixture; its name is the query.
    await page.getByTestId("crossbar-input").fill("go.mod");
    const fileRow = page.getByTestId("crossbar-inventory-file");
    await expect(fileRow.first()).toBeVisible({ timeout: 15_000 });
    await expect(fileRow.first()).toContainText("go.mod");
    await expect(fileRow.first()).toContainText("File");

    await fileRow.first().click();
    await expect(page.getByTestId("crossbar")).toHaveCount(0);
    // Opens the file, not the depth stage.
    await expect(page.getByTestId("global-search-view")).toHaveCount(0);
  });

  webE2e("Find everywhere opens the evidence search mode", async ({ page }) => {
    test.setTimeout(120_000);
    await bootstrapChatSession(page);

    await page.keyboard.press("ControlOrMeta+Shift+f");
    await expect(page.getByTestId("crossbar")).toBeVisible({ timeout: 10_000 });
    await expect(page.getByTestId("crossbar-mode-evidence")).toHaveAttribute(
      "aria-selected",
      "true",
    );
    await expect(page.getByTestId("crossbar-mode-actions")).toHaveAttribute(
      "aria-selected",
      "false",
    );
    // Every result family is a tab.
    for (const group of ["messages", "files", "symbols", "code", "evidence"]) {
      await expect(page.getByTestId(`crossbar-mode-${group}`)).toBeVisible();
    }
  });
});
