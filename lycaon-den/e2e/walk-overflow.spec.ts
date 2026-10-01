import { expect } from "@playwright/test";
import { writeFileSync } from "node:fs";
import path from "node:path";
import { modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";

modelIndependentWebE2e("long walks browse, jump by recognizable content, and retain keyboard focus", async ({ page, request }, testInfo) => {
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "walk-overflow", name: "Walk overflow",
    seed(root) {
      for (let i = 0; i < 12; i++) writeFileSync(path.join(root, `existing-file-${i}.ts`), "export {};\n");
    },
  });
  await page.evaluate(async ({ projectId, rootId }) => {
    const navigationUrl = "/src/platform/navigation/open-source.ts";
    const navigation = await import(/* @vite-ignore */ navigationUrl) as typeof import("../src/platform/navigation/open-source.ts");
    for (let i = 0; i < 12; i++) await navigation.openSourceLocation({
      projectId, rootId, path: `existing-file-${i}.ts`, intent: "permanent",
    });
  }, { projectId: project.id, rootId: project.roots[0]!.id });
  await expect.poll(() => page.evaluate(() => document.body.scrollHeight - innerHeight)).toBe(0);
  await page.evaluate(() => {
    const frames: { rail: boolean; transport: boolean; complete: boolean }[] = [];
    let frame = 0;
    const visible = (element: Element | null) => {
      if (!element?.getClientRects().length) return false;
      for (let ancestor: Element | null = element; ancestor; ancestor = ancestor.parentElement) {
        const style = getComputedStyle(ancestor);
        if (style.display === "none" || style.visibility === "hidden" || Number(style.opacity) === 0) return false;
      }
      return true;
    };
    const sample = () => {
      const rail = visible(document.querySelector('[data-testid="step-bar"]'));
      const transport = visible(document.querySelector('[data-testid="walk-transport"]'));
      frames.push({ rail, transport, complete: !rail || Boolean(
        document.querySelector('[data-testid="step-bar-read"]') &&
        document.querySelector('[data-testid="walk-chapter-heading"]') &&
        document.querySelector('[data-testid="step-bar-dot"]') &&
        document.querySelector('.den-files-tab-deck [data-resident="active"][data-presentation="published"]')
      ) });
      frame = requestAnimationFrame(sample);
    };
    (window as unknown as { walkPaintAudit: { stop: () => typeof frames } }).walkPaintAudit = {
      stop: () => { cancelAnimationFrame(frame); return frames; },
    };
    sample();
  });
  await page.evaluate(async (projectId) => {
    const walkUrl = "/src/files/walk/walk-store.ts";
    const fixtureUrl = "/src/files/walk/walk-fixtures.ts";
    const walk = await import(/* @vite-ignore */ walkUrl) as typeof import("../src/files/walk/walk-store.ts");
    const fixture = await import(/* @vite-ignore */ fixtureUrl) as typeof import("../src/files/walk/walk-fixtures.ts");
    const response = fixture.walkResponseFixture(Array.from({ length: 240 }, (_, index) => ({
      ...fixture.walkEffectFixture(`effect-${index}`, Math.floor(index / 10) + 1, index + 1),
      command_id: `command-${index}`, origin: "external", cause: "command_window",
    })));
    response.commands = Array.from({ length: 240 }, (_, index) => ({
      id: `command-${index}`, session_id: "s1", turn: Math.floor(index / 10) + 1,
      tool_call_id: `call-${index}`, tool_name: "command", command_line: `rg feature-${index} src/`,
      state: "ended", admission_mode: "scope_bounded", ordinal: index + 1,
      started_at: "2026-09-03T00:00:00Z", ended_at: "2026-09-03T00:00:01Z",
    }));
    response.turns = Array.from({ length: 24 }, (_, index) => ({
      ...fixture.walkTurnFixture(index + 1), prompt: index === 16 ? "Add keyboard navigation to file history" : `Review feature ${index + 1} and its tests, including a very long description of the files and commands that should stay within the history picker`,
    }));
    const client = fixture.walkClientFixture(() => response);
    client.listProjectSourceWalk = async () => {
      // Two frames expose loading to the paint audit.
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())));
      return response;
    };
    await walk.enterWalk(projectId, client, "s1", null, { startMessageId: "s1-user-1" });
    (window as unknown as { appendWalkStep: () => Promise<unknown> }).appendWalkStep = async () => {
      response.commands.push({ ...response.commands[response.commands.length - 1]!,
        id: "command-arrival", tool_call_id: "call-arrival", ordinal: 241,
      });
      response.files[0]!.effects.push({ ...fixture.walkEffectFixture("effect-arrival", 24, 241),
        command_id: "command-arrival", origin: "external", cause: "command_window",
      });
      await walk.refreshWalk(projectId, client);
    };
  }, project.id);
  const rail = page.getByTestId("step-bar-rail");
  const counter = page.getByTestId("step-bar-read");
  const openHistory = async () => {
    await expect(page.getByTestId("step-bar")).toHaveAttribute("data-boot", "ready");
    await counter.click();
    await expect(counter).toHaveAttribute("aria-expanded", "true");
    const picker = page.getByRole("dialog", { name: "Jump in walk", includeHidden: true });
    await expect(picker).toBeVisible();
    await expect(picker).toHaveCSS("opacity", "1");
  };
  const current = page.locator('[data-testid="step-bar-dot"][aria-current="step"]');
  await expect(counter).toContainText("1 of 240");
  await expect(rail).toBeVisible();
  expect(await page.getByTestId("step-bar-dot").count()).toBeLessThan(60);
  const selectedTabVisible = () => page.locator(".den-files-tab--active").evaluate((tab) => {
    const viewport = tab.closest(".den-files-tabs-scroller")!.getBoundingClientRect();
    const rect = tab.getBoundingClientRect();
    return { visible: rect.left >= viewport.left - 1 && rect.right <= viewport.right + 1,
      left: rect.left, right: rect.right, viewportLeft: viewport.left, viewportRight: viewport.right,
      scrollLeft: tab.closest(".den-files-tabs-scroller")!.scrollLeft,
      scrollWidth: tab.closest(".den-files-tabs-scroller")!.scrollWidth,
      clientWidth: tab.closest(".den-files-tabs-scroller")!.clientWidth,
      overflowWidth: document.querySelector('[data-testid="files-tabs-overflow"]')!.getBoundingClientRect().width };
  });
  const expectSelectedTabVisible = async () => {
    try {
      await expect.poll(selectedTabVisible).toMatchObject({ visible: true });
    } catch (error) {
      throw new Error(`${error}\nSelected tab geometry: ${JSON.stringify(await selectedTabVisible())}`);
    }
  };
  await expectSelectedTabVisible();
  await page.locator(".den-files-tabs-scroller").evaluate((element) => { element.scrollLeft = 0; });
  await expect.poll(selectedTabVisible).toMatchObject({ visible: false });
  await current.click();
  await expectSelectedTabVisible();
  const geometry = () => page.evaluate(() => {
    const bounds = (testId: string) => {
      const rect = document.querySelector(`[data-testid="${testId}"]`)!.getBoundingClientRect();
      return { left: rect.left, width: rect.width };
    };
    return {
      counter: bounds("step-bar-read"), rail: bounds("step-bar-rail"), transport: bounds("walk-transport"),
    };
  });
  const initialGeometry = await geometry();
  // Digit changes share a fixed counter width.
  for (const index of [8, 98]) {
    await page.evaluate(async ({ projectId, index }) => {
      const walkUrl = "/src/files/walk/walk-store.ts";
      const walk = await import(/* @vite-ignore */ walkUrl) as typeof import("../src/files/walk/walk-store.ts");
      walk.setWalkAt(projectId, index);
    }, { projectId: project.id, index });
    await expect(counter).toContainText(`${index + 1} of 240`);
    const offset = await rail.evaluate((element) => element.scrollLeft);
    await page.getByTestId("walk-transport-next").click();
    await expect(counter).toContainText(`${index + 2} of 240`);
    expect(await geometry()).toEqual(initialGeometry);
    expect(await rail.evaluate((element) => element.scrollLeft)).toBe(offset);
  }
  await page.getByTestId("walk-transport-first").click();
  await expect(counter).toContainText("1 of 240");
  await page.getByRole("button", { name: "Browse later steps" }).click();
  await expect.poll(() => rail.evaluate((element) => element.scrollLeft)).toBeGreaterThan(100);
  await expect(counter).toContainText("1 of 240");
  await openHistory();
  await page.screenshot({ path: testInfo.outputPath("walk-history-initial.png"), fullPage: true });
  const history = page.getByRole("toolbar", { name: "Changes by turn" });
  await expect(history.locator("button[data-walk-destination]")).toHaveCount(240);
  await expect.poll(() => history.evaluate((element) => element.scrollHeight > element.clientHeight)).toBe(true);
  const searchTop = await page.getByRole("searchbox").evaluate((element) => element.getBoundingClientRect().top);
  await history.evaluate((element) => { element.scrollTop = element.scrollHeight; });
  await expect.poll(() => page.getByRole("searchbox").evaluate((element) => element.getBoundingClientRect().top)).toBe(searchTop);
  await page.getByRole("button", { name: /rg feature-166.*Step 167/ }).click();
  await expect(counter).toContainText("167 of 240");
  await expect(counter).toBeFocused();
  await expect.poll(() => current.evaluate((dot) => {
    const rail = dot.closest(".den-step-bar__rail")!.getBoundingClientRect();
    const marker = dot.getBoundingClientRect();
    return { visible: marker.left >= rail.left && marker.right <= rail.right, markerLeft: marker.left, markerRight: marker.right, railLeft: rail.left, railRight: rail.right };
  })).toMatchObject({ visible: true });
  await current.focus();
  await current.press("End");
  await expect(counter).toContainText("240 of 240");
  await expect(current).toBeFocused();
  await current.press("Home");
  await expect(counter).toContainText("1 of 240");
  await expect(current).toBeFocused();
  await openHistory();
  await page.getByRole("searchbox", { name: "Find in walk" }).fill("keyboard navigation");
  await expect(page.getByRole("button", { name: /Step 161/ })).toBeVisible();
  await page.getByRole("searchbox").press("Escape");
  await expect(counter).toBeFocused();
  await expect(page.getByRole("dialog", { name: "Jump in walk" })).toHaveCount(0);
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 800, height: 900 });
  await page.evaluate(() => document.documentElement.style.setProperty("--den-text-scale", String(53 / 17)));
  await page.getByTestId("walk-transport-last").click();
  await expect(counter).toContainText("240 of 240");
  await expect.poll(() => current.evaluate((dot) => {
    const bounds = dot.getBoundingClientRect();
    const rail = dot.closest(".den-step-bar__rail")!.getBoundingClientRect();
    return bounds.left >= rail.left - 1 && bounds.right <= rail.right + 1;
  })).toBe(true);
  await page.screenshot({ path: testInfo.outputPath("walk-overflow-large-text.png"), fullPage: true });
  await openHistory();
  const picker = page.getByRole("dialog", { name: "Jump in walk" });
  await expect.poll(() => picker.evaluate((element) => ({ horizontal: element.scrollWidth > element.clientWidth, vertical: element.scrollHeight > element.clientHeight }))).toEqual({ horizontal: false, vertical: false });
  await expect.poll(() => history.evaluate((element) => element.scrollHeight > element.clientHeight)).toBe(true);
  await expect(picker.locator('[aria-current="step"]')).toBeInViewport();
  await page.screenshot({ path: testInfo.outputPath("walk-history-picker-large-text.png"), fullPage: true });
  await page.getByRole("searchbox").press("Escape");
  await page.evaluate(() => document.documentElement.style.removeProperty("--den-text-scale"));
  await page.setViewportSize({ width: 1280, height: 900 });
  await expect.poll(() => current.evaluate((dot) => {
    const bounds = dot.getBoundingClientRect();
    const rail = dot.closest(".den-step-bar__rail")!.getBoundingClientRect();
    return bounds.left >= rail.left - 1 && bounds.right <= rail.right + 1;
  })).toBe(true);
  await openHistory();
  await expect.poll(() => picker.evaluate((element) => ({ horizontal: element.scrollWidth > element.clientWidth, vertical: element.scrollHeight > element.clientHeight }))).toEqual({ horizontal: false, vertical: false });
  await expect.poll(() => picker.locator(".den-walk-navigator__description").first().evaluate((element) => ({ clipped: element.scrollWidth > element.clientWidth, overflow: getComputedStyle(element).textOverflow }))).toEqual({ clipped: true, overflow: "ellipsis" });
  await page.screenshot({ path: testInfo.outputPath("walk-history-picker.png"), fullPage: true });
  await page.getByRole("searchbox").press("Escape");
  const refresh = page.getByTestId("walk-transport-refresh");
  const arrivalGeometry = () => page.evaluate(() => {
    const bounds = (id: string) => {
      const rect = document.querySelector(`[data-testid="${id}"]`)!.getBoundingClientRect();
      return { x: rect.x, y: rect.y, width: rect.width, height: rect.height };
    };
    return ["step-bar", "step-bar-rail", "walk-transport", "walk-transport-refresh"].map(bounds);
  });
  await expect(refresh).toBeDisabled();
  await expect(refresh).toHaveCSS("opacity", "0.34");
  const beforeArrival = await arrivalGeometry();
  const idleColor = await refresh.evaluate((element) => getComputedStyle(element).color);
  await page.evaluate(() => (window as unknown as { appendWalkStep: () => Promise<unknown> }).appendWalkStep());
  await expect(refresh).toBeEnabled();
  await expect(refresh).toHaveCSS("opacity", "1");
  await expect(refresh).toHaveAccessibleName("Show 1 new step");
  expect(await arrivalGeometry()).toEqual(beforeArrival);
  expect(await refresh.evaluate((element) => getComputedStyle(element).color)).not.toBe(idleColor);
  await refresh.click();
  await expect(refresh).toBeDisabled();
  await expect(counter).toContainText("241 of 241");
  expect(await arrivalGeometry()).toEqual(beforeArrival);
  await page.screenshot({ path: testInfo.outputPath("walk-refresh-controls.png"), fullPage: true });
  const transport = page.getByTestId("walk-transport");
  const grip = page.getByTestId("walk-transport-grip");
  await expect(transport).toHaveCSS("position", "absolute");
  const pane = await transport.evaluate(element => {
    const rect = element.closest(".den-files-editor-pane")!.getBoundingClientRect();
    return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom };
  });
  const dragGripTo = async (x: number, y: number) => {
    const rect = await grip.boundingBox();
    expect(rect).not.toBeNull();
    await page.mouse.move(rect!.x + rect!.width / 2, rect!.y + rect!.height / 2);
    await page.mouse.down();
    await page.mouse.move(x, y, { steps: 8 });
    await page.mouse.up();
  };
  await dragGripTo((pane.left + pane.right) / 2, pane.bottom - 5);
  await expect(transport).toHaveClass(/den-walk-transport--docked/);
  expect(await transport.evaluate(element => {
    const content = element.closest(".den-files-editor-pane")!.querySelector('[data-testid="files-pane-presentation"]')!.getBoundingClientRect();
    return element.getBoundingClientRect().top >= content.bottom - 1;
  })).toBe(true);
  await page.screenshot({ path: testInfo.outputPath("walk-controls-docked.png") });
  await dragGripTo(pane.left + 100, pane.top + 150);
  await expect(transport).not.toHaveClass(/den-walk-transport--docked/);
  await expect(transport).toHaveCSS("position", "absolute");
  await grip.press("Home");
  await page.getByTestId("walk-transport-close").click();
  await expect(page.getByTestId("step-bar")).toHaveCount(0);
  await expect(page.getByTestId("walk-transport")).toHaveCount(0);
  const frames = await page.evaluate(() => (window as unknown as {
    walkPaintAudit: { stop: () => { rail: boolean; transport: boolean; complete: boolean }[] };
  }).walkPaintAudit.stop());
  expect(frames.some((frame) => frame.rail)).toBe(true);
  expect(frames.filter((frame) => frame.rail !== frame.transport || !frame.complete)).toEqual([]);

  await page.evaluate(async (projectId) => {
    const walkUrl = "/src/files/walk/walk-store.ts";
    const fixtureUrl = "/src/files/walk/walk-fixtures.ts";
    const walk = await import(/* @vite-ignore */ walkUrl) as typeof import("../src/files/walk/walk-store.ts");
    const fixture = await import(/* @vite-ignore */ fixtureUrl) as typeof import("../src/files/walk/walk-fixtures.ts");
    const response = fixture.walkResponseFixture([fixture.walkEffectFixture("delayed", 1, 1)]);
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const client = fixture.walkClientFixture(() => response);
    client.listProjectSourceWalk = async () => { await gate; return response; };
    const entering = walk.enterWalk(projectId, client, "s1");
    (window as unknown as { releaseWalkEntry: () => Promise<unknown> }).releaseWalkEntry = () => { release(); return entering; };
  }, project.id);
  await page.getByTestId("walk-preparing").getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(page.getByTestId("step-bar")).toHaveCount(0);
  await page.evaluate(() => (window as unknown as { releaseWalkEntry: () => Promise<unknown> }).releaseWalkEntry());
  await expect(page.getByTestId("step-bar")).toHaveCount(0);
  await expect(page.getByTestId("walk-transport")).toHaveCount(0);
});

modelIndependentWebE2e("walk history scrolls through all turns and filters in place", async ({ page, request }, testInfo) => {
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "walk-history", name: "Walk history",
    seed(root) { writeFileSync(path.join(root, "a.ts"), "export {};\n"); },
  });
  await page.evaluate(async (projectId) => {
    const walkUrl = "/src/files/walk/walk-store.ts";
    const fixtureUrl = "/src/files/walk/walk-fixtures.ts";
    const walk = await import(/* @vite-ignore */ walkUrl) as typeof import("../src/files/walk/walk-store.ts");
    const fixture = await import(/* @vite-ignore */ fixtureUrl) as typeof import("../src/files/walk/walk-fixtures.ts");
    const response = fixture.walkResponseFixture(Array.from({ length: 240 }, (_, index) => ({
      ...fixture.walkEffectFixture(`history-${index}`, Math.floor(index / 10) + 1, index + 1),
      path: `src/${"long-file-name-with-many-sections-".repeat(20)}.ts`,
      from_path: `src/${"previous-file-name-with-many-sections-".repeat(20)}.ts`,
    })));
    await walk.enterWalk(projectId, fixture.walkClientFixture(() => response), "s1", null, { startMessageId: "s1-user-1" });
  }, project.id);
  const counter = page.getByTestId("step-bar-read");
  const status = page.locator(".den-files-editor__status:visible");
  await expect(status).toBeVisible();
  const transport = page.getByTestId("walk-transport");
  const grip = page.getByTestId("walk-transport-grip");
  await grip.press("End");
  await expect(transport).toHaveClass(/den-walk-transport--docked/);
  await expect(transport).toHaveCSS("border-left-width", "1px");
  await expect.poll(async () => Math.abs((await transport.boundingBox())!.y + (await transport.boundingBox())!.height - (await status.boundingBox())!.y)).toBeLessThanOrEqual(1);
  await page.screenshot({ path: testInfo.outputPath("walk-controls-above-status.png") });
  await grip.press("Home");
  await counter.click();
  const picker = page.getByRole("dialog", { name: "Jump in walk" });
  const search = picker.getByRole("searchbox");
  const history = picker.getByRole("toolbar", { name: "Changes by turn" });
  const rows = history.locator("button[data-walk-destination]");
  await expect(picker.locator(".den-walk-navigator__list > .os-scrollbar-vertical")).toHaveCount(1);
  await expect(rows).toHaveCount(240);
  await expect.poll(() => history.evaluate((element) => element.scrollHeight > element.clientHeight)).toBe(true);
  const horizontalBounds = () => history.evaluate((element) => {
    element.scrollLeft = 100;
    return { overflow: element.scrollWidth > element.clientWidth, left: element.scrollLeft };
  });
  await expect.poll(horizontalBounds).toEqual({ overflow: false, left: 0 });
  const label = rows.first().locator(".den-walk-navigator__path");
  await expect.poll(() => label.evaluate((element) => ({ clipped: element.scrollWidth > element.clientWidth, overflow: getComputedStyle(element).textOverflow }))).toEqual({ clipped: true, overflow: "ellipsis" });
  await expect.poll(() => rows.first().evaluate((element) => {
    const title = element.querySelector(".den-walk-navigator__path")!.getBoundingClientRect();
    const count = element.querySelector(".den-walk-navigator__row-head > span")!.getBoundingClientRect();
    return count.left - title.right;
  })).toBeGreaterThanOrEqual(8);
  const searchTop = await search.evaluate((element) => element.getBoundingClientRect().top);
  await history.evaluate((element) => { element.scrollTop = element.scrollHeight; });
  await expect(rows.last()).toBeInViewport();
  expect(await search.evaluate((element) => element.getBoundingClientRect().top)).toBe(searchTop);
  await search.fill("Make change 20");
  await expect(rows).toHaveCount(10);
  await expect(history.getByText("Turn 20", { exact: true })).toBeVisible();
  await expect(history.getByText("Turn 19", { exact: true })).toHaveCount(0);
  await search.fill("");
  await expect(rows).toHaveCount(240);
  await search.press("ArrowDown");
  await rows.first().press("End");
  await expect(rows.last()).toBeFocused();
  await expect(rows.last()).toBeInViewport();
  await page.screenshot({ path: testInfo.outputPath("walk-history-scroll.png"), fullPage: true });
  await rows.last().click();
  await expect(counter).toContainText("240 of 240");
  await expect(counter).toBeFocused();
  await page.setViewportSize({ width: 800, height: 900 });
  await page.evaluate(() => document.documentElement.style.setProperty("--den-text-scale", String(53 / 17)));
  await counter.click();
  await expect(picker.locator('[aria-current="step"]')).toBeInViewport();
  await expect.poll(() => picker.evaluate((element) => ({ horizontal: element.scrollWidth > element.clientWidth, vertical: element.scrollHeight > element.clientHeight }))).toEqual({ horizontal: false, vertical: false });
  await expect.poll(horizontalBounds).toEqual({ overflow: false, left: 0 });
  await expect(search).toBeInViewport();
  await page.screenshot({ path: testInfo.outputPath("walk-history-scroll-large-text.png"), fullPage: true });
});

modelIndependentWebE2e("walk controls retain docking and floating coordinates across reloads", async ({ page, request }, testInfo) => {
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "walk-placement", name: "Walk placement",
    seed(root) { writeFileSync(path.join(root, "a.ts"), "export {};\n"); },
  });
  const enter = async () => {
    await page.evaluate(async projectId => {
    const walkUrl = "/src/files/walk/walk-store.ts";
    const fixtureUrl = "/src/files/walk/walk-fixtures.ts";
    const walk = await import(/* @vite-ignore */ walkUrl) as typeof import("../src/files/walk/walk-store.ts");
    const fixture = await import(/* @vite-ignore */ fixtureUrl) as typeof import("../src/files/walk/walk-fixtures.ts");
    await walk.enterWalk(projectId, fixture.walkClientFixture(() => fixture.walkResponseFixture([
      fixture.walkEffectFixture("placement", 1, 1),
    ])), "s1");
    }, project.id);
    await expect(page.getByTestId("walk-transport")).toHaveAttribute("data-boot", "ready");
  };
  const flush = () => page.evaluate(async () => {
    const stateUrl = "/src/store/app-state-snapshot.ts";
    const state = await import(/* @vite-ignore */ stateUrl) as typeof import("../src/store/app-state-snapshot.ts");
    await state.flushAppState();
  });
  const reopen = async () => {
    await flush();
    await page.reload();
    await page.getByTestId("project-files-entry").click();
    await enter();
  };
  await enter();
  const transport = page.getByTestId("walk-transport");
  const grip = page.getByTestId("walk-transport-grip");
  await grip.press("End");
  await expect(transport).toHaveClass(/den-walk-transport--docked/);
  await reopen();
  await expect(transport).toHaveClass(/den-walk-transport--docked/);
  await grip.press("Home");
  await grip.press("ArrowUp");
  await grip.press("ArrowLeft");
  const coordinates = () => transport.evaluate(element => ({ left: (element as HTMLElement).style.left, top: (element as HTMLElement).style.top }));
  const saved = await coordinates();
  await reopen();
  await expect(transport).not.toHaveClass(/den-walk-transport--docked/);
  await expect.poll(coordinates).toEqual(saved);
  await page.setViewportSize({ width: 800, height: 600 });
  await expect.poll(() => transport.evaluate(element => {
    const host = element.closest(".den-files-editor-pane")!.getBoundingClientRect();
    const unit = element.firstElementChild!.getBoundingClientRect();
    return unit.left >= host.left && unit.right <= host.right && unit.top >= host.top && unit.bottom <= host.bottom;
  })).toBe(true);
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await page.getByTestId("settings-nav-general").click();
  await expect(page.getByTestId("shell-workspace-veil")).toHaveCount(0);
  await page.getByTestId("general-tab-editor").click();
  await page.getByTestId("editor-walk-controls-docked").click();
  await expect(page.getByTestId("editor-walk-controls-reset")).toBeDisabled();
  await flush();
  await expect.poll(() => page.evaluate(async () => {
    const prefsUrl = "/src/platform/persistence/app-state.ts";
    const prefs = await import(/* @vite-ignore */ prefsUrl) as typeof import("../src/platform/persistence/app-state.ts");
    const editor = (await prefs.loadAppState()).editor;
    return { default: editor?.walkControlsDefault, location: editor?.walkControlsLocation ?? null };
  })).toEqual({ default: "docked", location: null });
  await expect(page.getByTestId("editor-walk-controls-docked")).toHaveAttribute("aria-pressed", "true");
  await expect.poll(() => page.getByTestId("editor-walk-controls-default").evaluate(element => {
    const thumb = element.querySelector(".den-browse-segment-thumb")!.getBoundingClientRect();
    const selected = element.querySelector('[aria-pressed="true"]')!.getBoundingClientRect();
    return Math.abs(thumb.left - selected.left);
  })).toBeLessThanOrEqual(1);
  await page.screenshot({ path: testInfo.outputPath("walk-placement-settings.png") });
});
