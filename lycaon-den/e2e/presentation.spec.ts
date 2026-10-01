import { expect } from "@playwright/test";
import { bootstrapChatSession, openNewChatSession, webE2e } from "./helpers.ts";

webE2e.describe("presentation stability", () => {
  webE2e("keeps the Files overview painted when returning to the stage", async ({ page }) => {
    await bootstrapChatSession(page);
    const nav = page.getByTestId("focused-project-nav");
    const files = nav.getByRole("button", { name: "Files", exact: true });
    await files.click();
    const overview = page.getByTestId("files-root-summary");
    await expect(overview).toBeVisible();
    await expect.poll(() => overview.evaluate((element) =>
      getComputedStyle(element.closest(".den-stage-boot")!).opacity,
    )).toBe("1");
    const surface = overview.locator("xpath=ancestor::*[@data-resident-key][1]");
    const original = await overview.elementHandle();
    expect(original).not.toBeNull();
    await nav.getByRole("button", { name: "Search", exact: true }).click();
    await expect(surface).toHaveAttribute("data-resident", "idle");

    const refreshed = page.waitForResponse((response) =>
      new URL(response.url()).pathname.endsWith("/source/workspace") && response.ok(),
    );
    await files.click();
    await refreshed;
    await expect(surface).toHaveAttribute("data-resident", "active");
    const failures = await original!.evaluate(async (element) => {
      const failures: string[] = [];
      for (let frame = 0; frame < 30; frame++) {
        await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
        if (!element.isConnected) {
          failures.push("overview remounted");
          break;
        }
        if (!element.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true })) {
          failures.push("overview hidden");
        }
        const reveal = element.closest(".den-stage-boot")!;
        const opacity = getComputedStyle(reveal).opacity;
        if (opacity !== "1") failures.push(`overview fading: ${opacity}, ${reveal.getAnimations().map((animation) => animation.currentTime).join(",")}`);
      }
      return failures;
    });
    expect(failures).toEqual([]);
  });

  webE2e("keeps Chat painted while inline Files opens cold", async ({ page }) => {
    await bootstrapChatSession(page);
    const chat = page.getByTestId("chat-view");
    const chatSurface = chat.locator(
      "xpath=ancestor::*[@data-resident-key][1]",
    );
    let releaseBrowse!: () => void;
    const browseHeld = new Promise<void>((resolve) => {
      releaseBrowse = resolve;
    });
    let browseRequested = false;
    await page.route("**/source/browse?**", async (route) => {
      browseRequested = true;
      await browseHeld;
      await route.continue();
    });
    try {
      await page
        .getByTestId("focused-project-nav")
        .getByRole("button", { name: "Files", exact: true })
        .click();
      await expect.poll(() => browseRequested).toBe(true);
      await expect(chat).toBeVisible();
      await expect(chatSurface).toHaveAttribute("data-retained", "true");
      await expect(
        chatSurface.locator("xpath=../*[contains(@class, 'den-presentation-wait')]")
      ).toBeVisible();

      releaseBrowse();
      await expect(
        page.getByRole("navigation", { name: "Project files" }),
      ).toBeVisible();
      await expect(chat).toBeHidden();
    } finally {
      releaseBrowse();
      await page.unrouteAll({ behavior: "wait" });
    }
  });

  for (const mode of ["inline", "split"] as const) {
    webE2e(`keeps Files and the sidebar stable through cold Security preparation in ${mode} view`, async ({ page }) => {
      await page.setViewportSize({ width: 1500, height: 900 });
      await bootstrapChatSession(page);
      const nav = page.getByTestId("focused-project-nav");
      let releaseIndex!: () => void;
      const indexHeld = new Promise<void>((resolve) => { releaseIndex = resolve; });
      await page.route("**/source/index?**", async (route) => {
        await indexHeld;
        await route.continue();
      });
      let releaseScanners!: () => void;
      const scannersHeld = new Promise<void>((resolve) => { releaseScanners = resolve; });
      let scannerReads = 0;
      let historyReads = 0;
      let workspaceReads = 0;
      await page.route("**/v1/scanners?**", async (route) => {
        scannerReads++;
        await scannersHeld;
        await route.continue();
      });
      await page.route("**/v1/projects/*/scans?**", async (route) => {
        if (new URL(route.request().url()).searchParams.get("limit") !== "6") {
          await route.continue();
          return;
        }
        historyReads++;
        await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ scans: [] }) });
      });
      page.on("request", (request) => {
        if (new URL(request.url()).pathname.endsWith("/source/workspace")) workspaceReads++;
      });
      try {
        await nav.getByRole("button", { name: "Files", exact: true }).click();
        const tree = page.getByTestId("files-tree");
        await expect(tree).toBeVisible();
        await expect(tree.getByRole("button", { name: "README.md", exact: true })).toBeVisible();
        await expect(page.getByText("Preparing project overview…", { exact: true })).toBeVisible();
        if (mode === "split") {
          await page.getByTestId("layout-dock-btn").click();
          await page.getByTestId("layout-tile-split").click();
          await expect(page.getByTestId("split-divider")).toBeVisible();
          await page.getByTestId("layout-dock-btn").click();
        }
        const workspaceReadsBefore = workspaceReads;
        await nav.getByRole("button", { name: "Security scanners", exact: true }).click();
        await expect.poll(() => scannerReads).toBe(1);
        await expect(tree).toBeVisible();
        await expect(page.getByTestId("security-findings-pane")).toBeHidden();
        const violations = await page.evaluate(async () => {
          const tree = document.querySelector<HTMLElement>('[aria-label="Project files"]')!;
          const nav = document.querySelector<HTMLElement>('[data-testid="focused-project-nav"]')!;
          const button = nav.querySelector<HTMLElement>('[data-testid="new-chat-btn"]')!;
          const initial = tree.textContent;
          const top = button.getBoundingClientRect().top;
          const failures: string[] = [];
          for (let frame = 0; frame < 12; frame++) {
            await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
            if (!tree.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true })) failures.push("tree hidden");
            if (tree.textContent !== initial) failures.push("tree changed");
            if (Math.abs(button.getBoundingClientRect().top - top) > 1) failures.push("sidebar moved");
          }
          return failures;
        });
        expect(violations).toEqual([]);
        expect(workspaceReads).toBe(workspaceReadsBefore);
        releaseScanners();
        await expect(page.getByTestId("security-findings-pane")).toBeVisible();
        await expect(tree.locator("xpath=ancestor::*[@data-resident-key][1]")).toHaveAttribute("aria-hidden", "true");
        await expect(tree.locator("xpath=ancestor::*[@data-resident-key][1]")).toHaveJSProperty("inert", true);
        expect(scannerReads).toBe(1);
        expect(historyReads).toBe(1);
        await nav.getByRole("button", { name: "Files", exact: true }).click();
        await expect(tree).toBeVisible();
      } finally {
        releaseIndex();
        releaseScanners();
        await page.unrouteAll({ behavior: "wait" });
      }
    });
  }

  webE2e("keeps the complete outgoing section until every required scanner projection arrives", async ({ page }) => {
    await bootstrapChatSession(page);
    await page.getByRole("button", { name: "Settings", exact: true }).click();
    await page.getByTestId("settings-nav-general").click();
    const outgoing = page.getByTestId("general-settings-panel");
    await expect(outgoing).toBeVisible();
    let release!: () => void;
    const held = new Promise<void>((resolve) => { release = resolve; });
    let requested = false;
    await page.route("**/v1/scanners/catalog", async (route) => {
      requested = true;
      await held;
      await route.continue();
    });
    try {
      await page.getByTestId("settings-nav-scanners").click();
      await expect.poll(() => requested).toBe(true);
      await expect(outgoing).toBeVisible();
      await expect(outgoing.locator("xpath=ancestor::*[@data-retained][1]")).toHaveAttribute("data-retained", "true");
      await expect(page.getByTestId("scanners-main")).toBeHidden();
      await expect(page.locator(".den-surface-deck > .den-presentation-wait")).toBeVisible();
      const violations = await page.evaluate(async () => {
        const frames: string[] = [];
        const visible = (selector: string) => {
          const element = document.querySelector<HTMLElement>(selector);
          return !!element && element.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true });
        };
        for (let frame = 0; frame < 12; frame++) {
          await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
          if (!visible('[data-testid="general-settings-panel"]')) frames.push("outgoing disappeared");
          if (visible('[data-testid="scanners-main"]')) frames.push("partial incoming content");
        }
        return frames;
      });
      expect(violations).toEqual([]);
      release();
      await expect(page.getByTestId("scanners-main")).toBeVisible();
      await expect(page.getByTestId("scanners-list")).toBeVisible();
      await expect(outgoing).toBeHidden();
      await page.getByTestId("settings-nav-general").click();
      await expect(outgoing).toBeVisible();
    } finally {
      release();
      await page.unroute("**/v1/scanners/catalog");
    }
  });

  webE2e("publishes a failed incoming section without fabricating an empty scanner inventory", async ({ page }) => {
    await bootstrapChatSession(page);
    await page.getByRole("button", { name: "Settings", exact: true }).click();
    await page.route("**/v1/scanners/catalog", (route) => route.fulfill({
      status: 503, contentType: "application/json",
      body: JSON.stringify({ code: "internal_error", message: "Scanner catalog temporarily unavailable" }),
    }));
    await page.getByTestId("settings-nav-scanners").click();
    await expect(page.getByTestId("scanners-load-error")).toBeVisible();
    await expect(page.getByTestId("scanners-main")).toBeHidden();
    await expect(page.getByTestId("scanners-empty-sast")).toHaveCount(0);
    await expect(page.getByTestId("settings-stage-close")).toBeEnabled();
  });

  webE2e("keeps the sidebar status chips painted through a chat switch", async ({ page }) => {
    await bootstrapChatSession(page);
    const chips = page.getByTestId("composer-status-chips");
    await expect(chips).toBeVisible();
    const posture = page.getByTestId("status-chip-posture");
    await expect(posture).toBeVisible();
    const postureLabel = await posture.textContent();
    await page.evaluate(() => {
      const host = document.querySelector<HTMLElement>(".focused-project-nav__status")!;
      const chips = host.querySelector<HTMLElement>('[data-testid="composer-status-chips"]')!;
      const probe = { removed: 0, hiddenFrames: 0, sampledFrames: 0, hiddenBy: [] as string[] };
      (window as unknown as { __chipsProbe: typeof probe }).__chipsProbe = probe;
      const hiddenAncestor = () => {
        if (!document.contains(chips)) return "detached";
        for (let el: HTMLElement | null = chips; el; el = el.parentElement) {
          const style = getComputedStyle(el);
          if (style.display === "none" || style.visibility === "hidden" || style.opacity === "0") {
            return `${el.tagName.toLowerCase()}.${[...el.classList].join(".")} display=${style.display} visibility=${style.visibility} opacity=${style.opacity}`;
          }
        }
        return "none";
      };
      const describe = (node: Node) => {
        const el = node as HTMLElement;
        return `${el.tagName?.toLowerCase() ?? node.nodeName}.${[...(el.classList ?? [])].join(".")}#${el.getAttribute?.("data-testid") ?? ""}`;
      };
      new MutationObserver((records) => {
        for (const record of records) {
          for (const node of record.removedNodes) {
            if (node === chips || node.contains(chips)) {
              probe.removed++;
              probe.hiddenBy.push(`removed ${describe(node)} from ${describe(record.target)}`);
            }
          }
        }
      }).observe(document.body, { childList: true, subtree: true });
      const sample = () => {
        probe.sampledFrames++;
        if (!chips.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true })) {
          probe.hiddenFrames++;
          const culprit = hiddenAncestor();
          if (!probe.hiddenBy.includes(culprit)) probe.hiddenBy.push(culprit);
        }
        if (probe.sampledFrames < 600) requestAnimationFrame(sample);
      };
      requestAnimationFrame(sample);
    });
    await openNewChatSession(page);
    const probe = await page.evaluate(
      () => (window as unknown as { __chipsProbe: { removed: number; hiddenFrames: number; sampledFrames: number; hiddenBy: string[] } }).__chipsProbe,
    );
    expect(probe.sampledFrames).toBeGreaterThan(0);
    expect(probe.removed).toBe(0);
    expect(probe.hiddenBy).toEqual([]);
    expect(probe.hiddenFrames).toBe(0);
    await expect(posture).toHaveText(postureLabel ?? "");
  });
});
