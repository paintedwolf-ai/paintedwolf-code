import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect, type Page } from "@playwright/test";
import { apiSeedSessionTranscript, bootstrapChatSession, modelIndependentWebE2e as test, openProjectFilesFixture, settledChatSessionId, transcriptMessages } from "./helpers.ts";

declare global {
  interface Window {
    performanceInvestigation: { reads: number; readMs: number; slow: unknown[]; stateChanges: { milliseconds: number; characters: number }[]; editors: Set<Element>; scrollbars: Set<Element> };
  }
}

// Tracks viewport identities separately from timing.
async function installProbe(page: Page, projectId?: string) {
  await page.evaluate(async projectId => {
    const p = { reads: 0, readMs: 0, slow: [] as unknown[], stateChanges: [] as { milliseconds: number; characters: number }[], editors: new Set<Element>(), scrollbars: new Set<Element>() };
    Object.assign(window, { performanceInvestigation: p });
    const computedStyle = window.getComputedStyle;
    window.getComputedStyle = function(...args) {
      const start = performance.now();
      const result = computedStyle.apply(window, args);
      const milliseconds = performance.now() - start;
      if (milliseconds >= 2) p.slow.push({ phase: "computed-style", milliseconds, stack: new Error().stack });
      return result;
    };
    for (const [prototype, names] of [
      [HTMLElement.prototype, ["offsetWidth", "offsetHeight", "offsetTop", "offsetLeft"]],
      [Element.prototype, ["clientWidth", "clientHeight", "scrollHeight", "scrollWidth", "scrollTop", "scrollLeft"]],
    ] as const) for (const name of names) {
      const descriptor = Object.getOwnPropertyDescriptor(prototype, name);
      if (!descriptor?.get) continue;
      const read = descriptor.get;
      Object.defineProperty(prototype, name, { ...descriptor, get() {
        const start = performance.now();
        const value = read.call(this);
        const milliseconds = performance.now() - start;
        if (milliseconds >= 2) p.slow.push({ phase: name, milliseconds, stack: new Error().stack });
        return value;
      } });
    }
    const focus = HTMLElement.prototype.focus;
    HTMLElement.prototype.focus = function(options) {
      const start = performance.now();
      focus.call(this, options);
      const milliseconds = performance.now() - start;
      if (milliseconds >= 2) p.slow.push({ phase: "focus", milliseconds, stack: new Error().stack });
    };
    if (projectId) {
      const hostUrl = "/src/files/editor/files-editor-host.ts";
      const buffersUrl = "/src/files/documents/files-buffer-state.ts";
      const host = await import(/* @vite-ignore */ hostUrl) as typeof import("../src/files/editor/files-editor-host.ts");
      const buffers = await import(/* @vite-ignore */ buffersUrl) as typeof import("../src/files/documents/files-buffer-state.ts");
      const key = buffers.projectFilesState(projectId).activeKey;
      const view = key ? host.getFilesEditorView(projectId, key) : undefined;
      if (!view) throw new Error("Active editor unavailable for performance probe");
      const prototype = Object.getPrototypeOf(view) as typeof view;
      const setState = prototype.setState;
      prototype.setState = function(state) {
        const start = performance.now();
        try { setState.call(this, state); }
        finally { p.stateChanges.push({ milliseconds: performance.now() - start, characters: state.doc.length }); }
      };
      const statePrototype = Object.getPrototypeOf(view.state) as typeof view.state;
      const update = statePrototype.update;
      statePrototype.update = function(...specs) {
        const start = performance.now();
        try { return update.call(this, ...specs); }
        finally {
          const milliseconds = performance.now() - start;
          if (milliseconds >= 2) p.slow.push({ phase: "state.update", milliseconds, stack: new Error().stack });
        }
      };
    }
    const rect = Element.prototype.getBoundingClientRect;
    Element.prototype.getBoundingClientRect = function () {
      const start = performance.now(); const result = rect.call(this); const ms = performance.now() - start;
      p.reads++; p.readMs += ms;
      if (ms >= 2) p.slow.push({ ms, cls: this.className, stack: new Error().stack });
      return result;
    };
    const collect = (node: Element) => {
      for (const selector of [".cm-editor", ".cm-editor .os-scrollbar-vertical"]) {
        const set = selector === ".cm-editor" ? p.editors : p.scrollbars;
        if (node.matches(selector)) set.add(node);
        node.querySelectorAll(selector).forEach(el => set.add(el));
      }
    };
    collect(document.body);
    new MutationObserver(records => records.forEach(r => r.addedNodes.forEach(n => { if (n instanceof Element) collect(n); })))
      .observe(document.body, { childList: true, subtree: true });
  }, projectId);
}
async function probeSnapshot(page: Page) {
  return page.evaluate(() => {
    const p = window.performanceInvestigation;
    return { reads: p.reads, readMs: p.readMs, slow: p.slow, stateChanges: p.stateChanges, editorIdentities: p.editors.size, scrollbarIdentities: p.scrollbars.size,
      mountedEditors: document.querySelectorAll(".cm-editor").length, idleEditors: document.querySelectorAll('[data-resident="idle"] .cm-editor').length,
      mountedScrollbars: document.querySelectorAll(".cm-editor .os-scrollbar-vertical").length };
  });
}
async function frames(page: Page, n = 12) {
  await page.evaluate(async count => { for (let i = 0; i < count; i++) await new Promise(requestAnimationFrame); }, n);
}

test("reuses bounded editor viewports through twenty-file navigation", async ({ page, request }, info) => {
  info.setTimeout(240_000);
  const names = Array.from({ length: 20 }, (_, i) => `probe-${String(i).padStart(2, "0")}.ts`);
  const { project } = await openProjectFilesFixture(page, request, { prefix: "navigation-investigation", name: "Navigation investigation", seed: root => {
    for (const name of names) writeFileSync(path.join(root, name), `// ${name}\n` + Array.from({ length: 600 }, (_, i) => `export const value${i} = "${"long text ".repeat(i % 11)}";`).join("\n"));
  }});
  const samples: unknown[] = [];
  const select = async (name: string, kind: string) => {
    const target = kind === "open" ? page.getByTestId("files-tree-file").filter({ hasText: name }) : page.locator(`[data-testid="files-tab"][data-path="${name}"]`);
    const result = await target.evaluate((el, name) => new Promise(resolve => {
      const start = performance.now();
      el.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      if (!el.matches('[data-testid="files-tab"]')) el.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
      const handlerMs = performance.now() - start;
      const check = () => {
        const host = [...document.querySelectorAll('[data-testid="files-editor-host"]')].find(n => n.closest('[data-resident="active"]') && n.textContent?.includes(`// ${name}`));
        if (host && document.querySelector('[data-testid="files-pane-presentation"]')?.getAttribute("data-pending") === "false") resolve({ handlerMs, frameMs: performance.now() - start });
        else if (performance.now() - start > 10_000) resolve({ handlerMs, timeout: true });
        else requestAnimationFrame(check);
      }; requestAnimationFrame(check);
    }), name);
    expect(result).not.toHaveProperty("timeout", true);
    samples.push({ name, kind, result });
    await frames(page, 4);
  };
  for (const name of names) await select(name, "open");
  for (const name of names) await select(name, "baseline-revisit");
  await installProbe(page, project.id);
  for (let pass = 0; pass < 2; pass++) for (const name of names) await select(name, "instrumented-revisit");
  const revisits = await probeSnapshot(page);
  for (let i = 0; i < names.length; i++) await page.getByTestId("files-tab-close").last().click();
  await frames(page, 30);
  const closed = await probeSnapshot(page);
  writeFileSync(info.outputPath("navigation.json"), JSON.stringify({ samples, revisits, closed }, null, 2));
  expect(await page.getByTestId("files-tab").count()).toBe(0);
  expect(revisits.editorIdentities).toBeLessThanOrEqual(9);
  expect(revisits.scrollbarIdentities).toBeLessThanOrEqual(9);
  expect(closed.mountedEditors).toBe(0);
});

test("keeps synchronized review reveals responsive and anchored", async ({ page, request }, info) => {
  info.setTimeout(180_000);
  await bootstrapChatSession(page, request);
  const sessionId = await settledChatSessionId(page);
  const messages = Array.from({ length: 40 }, (_, i) => {
    const assistant = crypto.randomUUID();
    return [
      { id: crypto.randomUUID(), role: "user" as const, content: `Inspect file ${i}`, ord: i * 3 + 1 },
      { id: assistant, role: "assistant" as const, content: "", tool_calls: [{ id: `call-${i}`, name: "read", args: { path: `file-${i}.ts` } }], ord: i * 3 + 2 },
      { id: crypto.randomUUID(), role: "tool" as const, content: "File contents\n".repeat(50), tool_result: { tool_call_id: `call-${i}`, assistant_message_id: assistant, tool: "read", content: "File contents\n".repeat(50) }, ord: i * 3 + 3 },
    ].map(m => ({ ...m, seq: m.ord, created_at: "2026-09-15T00:00:00Z" }));
  }).flat();
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages(messages));
  await expect(page.locator(".den-activity-span").first()).toBeAttached();
  await page.evaluate(async id => {
    const walkUrl = "/src/files/walk/walk-store.ts"; const walk = await import(/* @vite-ignore */ walkUrl) as typeof import("../src/files/walk/walk-store.ts");
    const fixturesUrl = "/src/files/walk/walk-fixtures.ts"; const fixtures = await import(/* @vite-ignore */ fixturesUrl) as typeof import("../src/files/walk/walk-fixtures.ts");
    const effects = Array.from({ length: 40 }, (_, i) => ({ ...fixtures.walkEffectFixture(String(i), 1, i + 1), session_id: id }));
    await walk.enterWalk("review-investigation", fixtures.walkClientFixture(() => fixtures.walkResponseFixture(effects)), id);
  }, sessionId);
  await installProbe(page);
  const samples: unknown[] = [];
  for (const at of [0, 25, 7, 38, 1, 39, 4, 23, 0, 39, 2, 20]) {
    const duration = await page.evaluate(async at => {
      const walkUrl = "/src/files/walk/walk-store.ts"; const walk = await import(/* @vite-ignore */ walkUrl) as typeof import("../src/files/walk/walk-store.ts");
      const start = performance.now(); walk.setWalkAt("review-investigation", at); return performance.now() - start;
    }, at);
    const mark = page.locator(`.den-activity-span:has([data-tool-call-id="call-${at}"]).den-walk-step-mark`);
    await expect(mark).toHaveCount(1); await expect(mark).toBeInViewport({ ratio: 1 });
    await frames(page, 15);
    const positions = await mark.evaluate(async el => { const a: number[] = []; for (let i = 0; i < 10; i++) { await new Promise(requestAnimationFrame); a.push(el.getBoundingClientRect().top); } return a; });
    const finalDrift = Math.max(...positions) - Math.min(...positions);
    samples.push({ at, handlerMs: duration, finalDrift });
    expect(finalDrift).toBeLessThan(1);
    expect(duration).toBeLessThan(100);
  }
  writeFileSync(info.outputPath("review.json"), JSON.stringify({ samples, probe: await probeSnapshot(page) }, null, 2));
});

test("preserves editable document undo after viewport eviction", async ({ page, request }) => {
  const names = Array.from({ length: 10 }, (_, index) => `document-${index}.ts`);
  await openProjectFilesFixture(page, request, {
    prefix: "viewport-undo", name: "Viewport undo", seed: root => {
      for (const name of names) writeFileSync(path.join(root, name), `// ${name}\nexport const value = 1;\n`);
    },
  });
  const content = page.locator('[data-testid="files-editor-host"] .cm-content:visible');
  await page.getByTestId("files-tree-file").filter({ hasText: names[0] }).dblclick();
  await expect(content).toHaveAttribute("contenteditable", "true");
  await content.click();
  await content.press("ControlOrMeta+End");
  await page.keyboard.insertText("\n// retained edit");
  await expect(content).toContainText("retained edit");
  for (const name of names.slice(1)) {
    await page.getByTestId("files-tree-file").filter({ hasText: name }).dblclick();
    await expect(content).toContainText(name);
  }
  await page.locator(`[data-testid="files-tab"][data-path="${names[0]}"]`).click();
  await expect(content).toHaveAttribute("contenteditable", "true");
  await expect(content).toContainText("retained edit");
  await content.click();
  await content.press("ControlOrMeta+z");
  await expect(content).not.toContainText("retained edit");
  await expect(content).toContainText("export const value = 1;");
});
