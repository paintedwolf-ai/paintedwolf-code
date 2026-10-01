import { expect } from "@playwright/test";
import { bootstrapChatSession, webE2e } from "./helpers.ts";

webE2e.describe("presentation stability", () => {
  for (const orientation of ["standard", "mirrored"] as const) {
    webE2e(`publishes Settings and split collapse together in ${orientation} orientation`, async ({ page }) => {
      let releaseSettings!: () => void;
      const settingsHeld = new Promise<void>((resolve) => { releaseSettings = resolve; });
      await page.route("**/v1/settings/file-summaries", async (route) => {
        await settingsHeld;
        await route.continue();
      });
      try {
        await page.setViewportSize({ width: 1500, height: 900 });
        await bootstrapChatSession(page);
        await page.getByTestId("focused-project-nav")
          .getByRole("button", { name: "Files", exact: true }).click();
        await expect(page.getByTestId("files-tree")).toBeVisible();
        await page.getByTestId("layout-dock-btn").click();
        await page.getByTestId("layout-tile-split").click();
        const mirror = page.getByTestId("layout-toggle-orientation");
        const mirrored = String(orientation === "mirrored");
        if ((await mirror.getAttribute("aria-pressed")) !== mirrored) await mirror.click();
        await expect(mirror).toHaveAttribute("aria-pressed", mirrored);
        await page.getByTestId("layout-dock-btn").click();
        const host = page.getByTestId("shell-stage-host");
        await expect(host).toHaveAttribute("data-stage-leading", String(orientation === "standard"));
        await expect(page.getByTestId("chat-view")).toBeVisible();

        for (const visit of ["cold", "prepared"]) {
          const violations = await page.evaluate(async () => {
            const host = document.querySelector<HTMLElement>('[data-testid="shell-stage-host"]')!;
            const stage = host.querySelector<HTMLElement>('[data-testid="split-col-stage"]')!;
            const chat = host.querySelector<HTMLElement>('[data-testid="split-col-chat"]')!;
            const files = host.querySelector<HTMLElement>('[data-testid="files-tree"]')!;
            const outgoing = files.closest<HTMLElement>('[data-resident-key]')!;
            const columns = [stage, chat].map((element) => ({
              element,
              before: element.getBoundingClientRect(),
            }));
            const failures: string[] = [];
            const button = [...document.querySelectorAll<HTMLButtonElement>(".den-shell-dock-link")]
              .find((element) => element.textContent?.trim() === "Settings")!;
            button.click();
            for (let frame = 0; frame < 30; frame++) {
              await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
              if (outgoing.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true })) {
                if (!host.classList.contains("den-shell-stage-host--split")) failures.push("outgoing Files expanded before Settings published");
                columns.forEach(({ element, before }, index) => {
                  const rect = element.getBoundingClientRect();
                  if (Math.abs(rect.width - before.width) > 1 || Math.abs(rect.x - before.x) > 1) {
                    failures.push(`outgoing column ${index} moved`);
                  }
                });
              }
              const settings = host.querySelector<HTMLElement>('[data-resident-key="settings"]');
              if (settings?.dataset.resident === "active" && settings.dataset.presentation === "published") {
                if (host.classList.contains("den-shell-stage-host--split")) failures.push("Settings published in split geometry");
              }
            }
            return failures;
          });
          expect(violations, visit).toEqual([]);
          if (visit === "cold") {
            await expect(page.getByTestId("general-settings-panel")).toBeHidden();
            await expect(page.getByTestId("split-divider")).toBeVisible();
            releaseSettings();
          }
          await expect(page.getByTestId("general-settings-panel")).toBeVisible();
          await expect(page.getByTestId("split-divider")).toHaveCount(0);
          await page.getByTestId("settings-stage-close").click();
          await expect(page.getByTestId("split-divider")).toBeVisible();
          await expect(page.getByTestId("files-tree")).toBeVisible();
          await expect(page.getByTestId("chat-view")).toBeVisible();
        }
      } finally {
        releaseSettings();
        await page.unrouteAll({ behavior: "wait" });
      }
    });
  }
});
