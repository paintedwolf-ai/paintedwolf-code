import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect, type Locator, type Page } from "@playwright/test";
import { modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";

// One-line JSON arrays whose every seventh item carries a long name, so some
// visual lines wrap.
function oneLineJson(items: number): string {
  const out: string[] = [];
  for (let i = 0; i < items; i++) {
    const long = i % 7 === 0 ? "lorem ipsum ".repeat(14) : "";
    out.push(`{"id":${i},"name":"item ${i} ${long}","tags":["a","b"]}`);
  }
  return `[${out.join(",")}]`;
}

const ITEMS_200_KB = 2_800;
const ITEMS_1_MB = 14_000;

type Probe = { content: Locator; scroller: Locator };

type Source = number | { name: string; text: string; alongside?: Record<string, string> };

async function openPretty(page: Page, request: Parameters<typeof openProjectFilesFixture>[1], source: Source): Promise<Probe> {
  const { name, text, alongside = {} } = typeof source === "number" ? { name: `one-line-${source}.json`, text: oneLineJson(source) } : source;
  const { project } = await openProjectFilesFixture(page, request, {
    prefix: "pretty-display",
    name: "Pretty display",
    seed: (root) => {
      writeFileSync(path.join(root, name), text);
      for (const [file, body] of Object.entries(alongside)) writeFileSync(path.join(root, file), body);
    },
  });
  await page.getByTestId("files-tree-file").filter({ hasText: name }).click();
  const host = page.getByTestId("files-editor-host");
  const content = host.locator(".cm-content");
  await expect(content).toBeEditable();
  await page.evaluate(async (projectId) => {
    const host = await import(/* @vite-ignore */ String("/src/files/editor/files-editor-host.ts")) as typeof import("../src/files/editor/files-editor-host.ts");
    const buffers = await import(/* @vite-ignore */ String("/src/files/documents/files-buffer-state.ts")) as typeof import("../src/files/documents/files-buffer-state.ts");
    const pretty = await import(/* @vite-ignore */ String("/src/components/source/pretty/pretty-view.ts")) as typeof import("../src/components/source/pretty/pretty-view.ts");
    const view = () => host.getFilesEditorView(projectId, buffers.projectFilesState(projectId).activeKey!)!;
    const breaksIn = (from: number, to: number): number[] => {
      const out: number[] = [];
      view().state.facet(pretty.prettyLayoutDecorations).between(from, to, (at, end, value) => {
        if (at === end && value.spec.widget instanceof pretty.PrettyBreakWidget) out.push(at);
      });
      return out;
    };
    Object.assign(window, { __pretty: { view, breaksIn, visualLineAt: pretty.prettyVisualLineAt, active: pretty.prettyLayoutActive } });
  }, project.id);
  await page.getByTestId("files-editor-layout-pretty").click();
  await expect(page.getByTestId("files-editor-layout-pretty")).toHaveAttribute("aria-pressed", "true");
  await content.focus();
  return { content, scroller: host.locator(".cm-scroller") };
}

type PrettyProbe = {
  view: () => import("@codemirror/view").EditorView;
  breaksIn: (from: number, to: number) => number[];
  visualLineAt: typeof import("../src/components/source/pretty/pretty-view.ts").prettyVisualLineAt;
  active: typeof import("../src/components/source/pretty/pretty-view.ts").prettyLayoutActive;
};
declare global {
  interface Window { __pretty: PrettyProbe }
}

const settle = (page: Page) => page.evaluate(() => new Promise<void>((resolve) =>
  requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));

async function selection(page: Page) {
  return page.evaluate(() => {
    const { main } = window.__pretty.view().state.selection;
    return { anchor: main.anchor, head: main.head, assoc: main.assoc };
  });
}

async function caretTop(page: Page): Promise<number> {
  await settle(page);
  return page.evaluate(() => window.__pretty.view().dom.querySelector(".cm-cursor")!.getBoundingClientRect().top);
}

async function place(page: Page, pos: number) {
  await page.evaluate((pos) => window.__pretty.view().dispatch({ selection: { anchor: pos }, scrollIntoView: true }), pos);
  await settle(page);
}

/** Break positions in the rendered viewport, with the characters on each side. */
async function viewportBreaks(page: Page) {
  return page.evaluate(() => {
    const view = window.__pretty.view();
    const doc = view.state.doc;
    return window.__pretty.breaksIn(view.viewport.from, view.viewport.to).map((pos) => ({
      pos,
      before: doc.sliceString(pos - 1, pos),
      after: doc.sliceString(pos, pos + 1),
      line: window.__pretty.visualLineAt(view.state, pos, -1),
    }));
  });
}

async function coords(page: Page, pos: number, side: -1 | 1) {
  return page.evaluate(({ pos, side }) => {
    const rect = window.__pretty.view().coordsAtPos(pos, side);
    return rect && { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom };
  }, { pos, side });
}

modelIndependentWebE2e("lays a long one-line document out under soft wrapping", async ({ page, request }) => {
  const { content } = await openPretty(page, request, 4_000);
  const facts = await page.evaluate(() => {
    const view = window.__pretty.view();
    return {
      active: view.state.facet(window.__pretty.active),
      wrap: getComputedStyle(view.contentDOM).whiteSpace,
      lineNumberOnes: [...view.dom.querySelectorAll(".cm-gutters .cm-gutterElement")]
        .filter((element) => element.textContent?.trim() === "1").length,
    };
  });
  expect(facts.active).toBe(true);
  expect(["pre-wrap", "break-spaces"]).toContain(facts.wrap);
  expect(facts.lineNumberOnes).toBe(1);
  await expect(content).toBeFocused();
});

modelIndependentWebE2e("places the caret at every kind of visual line end by pointer", async ({ page, request }) => {
  await openPretty(page, request, 4_000);
  await place(page, 0);
  const breaks = await viewportBreaks(page);
  const pick = (test: (b: (typeof breaks)[number]) => boolean, label: string) => {
    const found = breaks.find((b) => test(b) && b.line.to - b.line.from < 60);
    expect(found, label).toBeTruthy();
    return found!;
  };
  const cases = [
    pick((b) => b.before === "[" || b.before === "{", "after an opener"),
    pick((b) => b.before === ",", "after a separator"),
    pick((b) => (b.after === "}" || b.after === "]") && b.before !== "}" && b.before !== "]", "after a value"),
    pick((b) => b.before === "]" || b.before === "}", "after a closer"),
  ];
  for (const b of cases) {
    const end = await coords(page, b.pos, -1);
    expect(end).not.toBeNull();
    await page.mouse.click(end!.right + 40, (end!.top + end!.bottom) / 2);
    expect(await selection(page), `trailing click at ${b.pos}`).toMatchObject({ head: b.pos, anchor: b.pos, assoc: -1 });
    expect(Math.abs((await caretTop(page)) - end!.top)).toBeLessThan(3);

    const start = await coords(page, b.pos, 1);
    await page.mouse.click(start!.left + 1, (start!.top + start!.bottom) / 2);
    expect(await selection(page), `leading click at ${b.pos}`).toMatchObject({ head: b.pos, assoc: 1 });
    expect(Math.abs((await caretTop(page)) - start!.top)).toBeLessThan(3);
    expect(start!.top).toBeGreaterThan(end!.top);
  }

  await page.keyboard.press(process.platform === "darwin" ? "Meta+ArrowDown" : "Control+End");
  await settle(page);
  const length = await page.evaluate(() => window.__pretty.view().state.doc.length);
  const last = await coords(page, length, -1);
  await page.mouse.click(last!.right + 40, (last!.top + last!.bottom) / 2);
  expect((await selection(page)).head).toBe(length);
});

modelIndependentWebE2e("edits at visual line ends and drags across breaks", async ({ page, request }) => {
  await openPretty(page, request, 4_000);
  await place(page, 0);
  const b = (await viewportBreaks(page)).find((x) => x.before === "," && x.line.to - x.line.from < 60)!;
  const end = await coords(page, b.pos, -1);
  await page.mouse.click(end!.right + 40, (end!.top + end!.bottom) / 2);
  const before = await page.evaluate(() => window.__pretty.view().state.doc.toString());
  await page.keyboard.type("7");
  expect(await page.evaluate((pos) => window.__pretty.view().state.doc.sliceString(pos, pos + 1), b.pos)).toBe("7");
  await page.keyboard.press("Backspace");
  await page.keyboard.press("Backspace");
  expect(await page.evaluate((pos) => window.__pretty.view().state.doc.sliceString(pos - 1, pos + 1), b.pos)).not.toContain(",");
  await page.keyboard.press(process.platform === "darwin" ? "Meta+z" : "Control+z");
  await page.keyboard.press(process.platform === "darwin" ? "Meta+z" : "Control+z");
  await expect.poll(() => page.evaluate(() => window.__pretty.view().state.doc.toString())).toBe(before);
  if (page.context().browser()?.browserType().name() === "chromium") {
    // Composition through the input method, committed at the visual line end.
    await page.mouse.click(end!.right + 40, (end!.top + end!.bottom) / 2);
    const ime = await page.context().newCDPSession(page);
    await ime.send("Input.imeSetComposition", { text: "に", selectionStart: 1, selectionEnd: 1 });
    await ime.send("Input.insertText", { text: "日" });
    await expect.poll(() => page.evaluate((pos) => window.__pretty.view().state.doc.sliceString(pos, pos + 1), b.pos)).toBe("日");
    expect((await selection(page)).head).toBe(b.pos + 1);
  }

  await place(page, 0);
  const breaks = await viewportBreaks(page);
  const from = breaks[2]!.pos + 2;
  const to = breaks[8]!.pos + 2;
  for (const [anchor, head] of [[from, to], [to, from]] as const) {
    // A press on an existing selection drags its text, so each drag starts from a caret.
    await page.evaluate(() => window.__pretty.view().dispatch({ selection: { anchor: 0 } }));
    await settle(page);
    const a = await coords(page, anchor, 1);
    const h = await coords(page, head, 1);
    await page.mouse.move(a!.left + 1, (a!.top + a!.bottom) / 2);
    await page.mouse.down();
    await page.mouse.move(h!.left + 1, (h!.top + h!.bottom) / 2, { steps: 12 });
    await page.mouse.up();
    expect(await selection(page)).toMatchObject({ anchor, head });
  }
});

modelIndependentWebE2e("moves by visual rows and stops line keys at the visual line", async ({ page, request }) => {
  const { content } = await openPretty(page, request, 4_000);
  await place(page, 0);
  const breaks = await viewportBreaks(page);
  const short = breaks.find((b) => b.before === "," && b.line.to - b.line.from > 8 && b.line.to - b.line.from < 40)!;
  const startPos = short.line.from + 3;
  const start = await coords(page, startPos, 1);
  await page.mouse.click(start!.left + 1, (start!.top + start!.bottom) / 2);
  expect((await selection(page)).head).toBe(startPos);

  let top = await caretTop(page);
  for (let i = 0; i < 6; i++) {
    await page.keyboard.press("ArrowDown");
    const next = await caretTop(page);
    expect(next).toBeGreaterThan(top);
    top = next;
  }
  for (let i = 0; i < 6; i++) await page.keyboard.press("ArrowUp");
  expect((await selection(page)).head).toBe(startPos);

  for (const key of ["End", "End", "End"]) {
    await page.keyboard.press(key);
    expect((await selection(page)).head).toBe(short.line.to);
  }
  for (const key of ["Home", "Home", "Home"]) {
    await page.keyboard.press(key);
    expect((await selection(page)).head).toBe(short.line.from);
  }
  await page.mouse.click(start!.left + 1, (start!.top + start!.bottom) / 2);
  await page.keyboard.press("Shift+End");
  await page.keyboard.press("Shift+End");
  expect(await selection(page)).toMatchObject({ anchor: startPos, head: short.line.to });
  if (process.platform === "darwin") {
    for (const [end, start_] of [["Meta+ArrowRight", "Meta+ArrowLeft"], ["Control+e", "Control+a"]] as const) {
      await page.mouse.click(start!.left + 1, (start!.top + start!.bottom) / 2);
      await page.keyboard.press(end);
      await page.keyboard.press(end);
      expect((await selection(page)).head, end).toBe(short.line.to);
      await page.keyboard.press(start_);
      await page.keyboard.press(start_);
      expect((await selection(page)).head, start_).toBe(short.line.from);
    }
  }

  const wrapped = await page.evaluate(() => {
    const view = window.__pretty.view();
    for (const pos of window.__pretty.breaksIn(view.viewport.from, view.viewport.to)) {
      const line = window.__pretty.visualLineAt(view.state, pos, 1);
      const first = view.coordsAtPos(line.from, 1);
      const last = view.coordsAtPos(line.to, -1);
      if (first && last && last.top - first.top > 4) return line;
    }
    return null;
  });
  expect(wrapped, "a visual line that wraps").not.toBeNull();
  const first = await coords(page, wrapped!.from, 1);
  await page.mouse.click(first!.left + 1, (first!.top + first!.bottom) / 2);
  await page.keyboard.press("End");
  const rowEnd = (await selection(page)).head;
  expect(rowEnd).toBeGreaterThan(wrapped!.from);
  expect(rowEnd).toBeLessThan(wrapped!.to);
  await page.keyboard.press("End");
  expect((await selection(page)).head).toBe(wrapped!.to);
  await page.keyboard.press("End");
  expect((await selection(page)).head).toBe(wrapped!.to);

  const rows = await page.evaluate(({ from, to }) => {
    const view = window.__pretty.view();
    const lefts = new Map<number, number>();
    for (let pos = from; pos < to; pos++) {
      const rect = view.coordsAtPos(pos, 1);
      if (!rect) continue;
      const top = Math.round(rect.top);
      lefts.set(top, Math.min(lefts.get(top) ?? Infinity, rect.left));
    }
    return [...lefts.values()];
  }, wrapped!);
  expect(rows.length).toBeGreaterThan(1);
  for (const left of rows) expect(Math.abs(left - rows[0]!)).toBeLessThan(1.5);
  await expect(content).toBeFocused();
});

modelIndependentWebE2e("shades exactly the visual line holding the caret", async ({ page, request }) => {
  await openPretty(page, request, 4_000);
  await place(page, 0);
  const breaks = await viewportBreaks(page);
  for (const b of [breaks[3]!, breaks[9]!]) {
    const start = await coords(page, b.pos, 1);
    await page.mouse.click(start!.left + 1, (start!.top + start!.bottom) / 2);
    await settle(page);
    const shade = await page.evaluate((pos) => {
      const view = window.__pretty.view();
      const markers = [...view.dom.querySelectorAll(".pw-pretty-active-line")];
      const line = window.__pretty.visualLineAt(view.state, pos, 1);
      const rect = markers[0]?.getBoundingClientRect();
      const first = view.coordsAtPos(line.from, 1)!;
      const last = view.coordsAtPos(line.to, -1)!;
      const activeLine = view.dom.querySelector(".cm-activeLine");
      return {
        count: markers.length,
        top: rect ? rect.top - first.top : null,
        bottom: rect ? rect.bottom - last.bottom : null,
        width: rect ? rect.width - view.contentDOM.clientWidth : null,
        sourceLineBackground: activeLine ? getComputedStyle(activeLine).backgroundColor : "none",
      };
    }, b.pos);
    expect(shade.count).toBe(1);
    expect(Math.abs(shade.top!)).toBeLessThan(3);
    expect(Math.abs(shade.bottom!)).toBeLessThan(3);
    expect(Math.abs(shade.width!)).toBeLessThan(2);
    expect(["none", "rgba(0, 0, 0, 0)", "transparent"]).toContain(shade.sourceLineBackground);
  }
});

modelIndependentWebE2e("keeps the selection and brings it into view across toggles", async ({ page, request }) => {
  await openPretty(page, request, 2_600);
  const length = await page.evaluate(() => window.__pretty.view().state.doc.length);
  const visible = () => page.evaluate(() => {
    const view = window.__pretty.view();
    const rect = view.coordsAtPos(view.state.selection.main.head);
    const port = view.scrollDOM.getBoundingClientRect();
    return Boolean(rect && rect.bottom > port.top && rect.top < port.bottom && rect.right >= port.left && rect.left <= port.right);
  });
  const toggles: Array<() => Promise<void>> = [
    async () => {
      const on = await page.getByTestId("files-editor-layout-pretty").getAttribute("aria-pressed") === "true";
      await page.getByTestId(on ? "files-editor-layout-off" : "files-editor-layout-pretty").click();
    },
    // Focus as the editor does: a native focus() scrolls the content element into view.
    async () => { await page.evaluate(() => window.__pretty.view().focus()); await page.keyboard.press("Alt+KeyP"); },
  ];
  for (const fraction of [0.3, 0.5, 0.9]) {
    for (const toggle of toggles) {
      const pos = Math.round(length * fraction);
      // A scroll request lands a position in a long wrapped line by its whole
      // block, so settle the head into view before toggling.
      await page.evaluate(async (pos) => {
        const view = window.__pretty.view();
        const scroll = await import(/* @vite-ignore */ String("/src/components/source/editor/editor-settled-scroll.ts")) as typeof import("../src/components/source/editor/editor-settled-scroll.ts");
        view.dispatch({ selection: { anchor: pos - 5, head: pos }, scrollIntoView: true });
        scroll.keepPositionInView(view, (state) => state.selection.main.head);
      }, pos);
      await expect.poll(visible, { timeout: 5_000 }).toBe(true);
      await settle(page);
      const before = await selection(page);
      const docBefore = await page.evaluate(() => window.__pretty.view().state.doc.length);
      for (let i = 0; i < 2; i++) {
        await page.evaluate(() => {
          const view = window.__pretty.view();
          const trace: number[][] = [];
          Object.assign(window, { __toggleTrace: trace });
          const tick = () => {
            const rect = view.coordsAtPos(view.state.selection.main.head);
            const port = view.scrollDOM.getBoundingClientRect();
            trace.push([Math.round(view.scrollDOM.scrollTop), rect ? Math.round(rect.top - port.top) : -1, Math.round(view.scrollDOM.scrollHeight)]);
            if (trace.length < 60) requestAnimationFrame(tick);
          };
          requestAnimationFrame(tick);
        });
        await toggle();
        expect(await selection(page)).toMatchObject({ anchor: before.anchor, head: before.head });
        try {
          await expect.poll(visible, { timeout: 5_000 }).toBe(true);
        } catch (error) {
          const trace = await page.evaluate(() => (window as unknown as { __toggleTrace: number[][] }).__toggleTrace);
          throw new Error(`toggle ${i} at ${fraction}: ${JSON.stringify(trace)}`, { cause: error });
        }
      }
      expect(await page.evaluate(() => window.__pretty.view().state.doc.length)).toBe(docBefore);
    }
  }
});

modelIndependentWebE2e("marks tabs in indentation while laid out", async ({ page, request }) => {
  const text = '{\n\t"rows": [\n\t\t{"a":\t1},\n  \t"mixed",\n\t\t"end"\n\t]\n}\n';
  await openPretty(page, request, { name: "tabs.json", text });
  const marks = (selector: string) => page.evaluate((selector) => [...window.__pretty.view().contentDOM.querySelectorAll(selector)]
    .map((element) => {
      const after = getComputedStyle(element, "::after");
      return { text: element.textContent, width: element.getBoundingClientRect().width, arrow: after.maskImage || after.webkitMaskImage };
    }), selector);
  const marked = await marks(".cm-den-tab");
  expect(marked).toHaveLength(7);
  for (const mark of marked) {
    expect(mark.text).toBe("\t");
    expect(mark.width).toBeGreaterThan(0);
    expect(mark.arrow).toContain("svg");
  }
  await page.getByTestId("files-editor-layout-off").click();
  await expect(page.getByTestId("files-editor-layout-pretty")).toHaveAttribute("aria-pressed", "false");
  await expect.poll(async () => (await marks(".cm-den-tab")).length).toBe(0);

  await page.evaluate(async () => {
    const prefs = await import(/* @vite-ignore */ String("/src/settings/editor/editor-prefs.ts")) as typeof import("../src/settings/editor/editor-prefs.ts");
    await prefs.saveEditorWhitespace(true);
  });
  await expect.poll(async () => (await marks(".cm-highlightTab")).length).toBe(8);
  for (const mark of await marks(".cm-highlightTab")) expect(mark.arrow).toContain("svg");
  const dots = await page.evaluate(() => [...window.__pretty.view().contentDOM.querySelectorAll(".cm-highlightSpace")]
    .map((element) => getComputedStyle(element).backgroundImage));
  expect(dots.length).toBeGreaterThan(0);
  for (const dot of dots) expect(dot).toContain("radial-gradient");
});

modelIndependentWebE2e("keeps the text at the top of the view across line layout changes when the caret is off screen", async ({ page, request }) => {
  // With Off the one-line document is unwrapped, so the place to keep is sideways.
  const { scroller } = await openPretty(page, request, ITEMS_1_MB);
  await page.evaluate(() => window.__pretty.view().dispatch({ selection: { anchor: 0 } }));
  await scroller.evaluate((element) => { element.scrollTop = (element.scrollHeight - element.clientHeight) / 2; });
  await settle(page);
  await settle(page);
  // The document position drawn at the top left of the view, and where it sits:
  // the first drawn line, which at the top of the document starts below the padding.
  const topText = () => page.evaluate(() => {
    const view = window.__pretty.view();
    const port = view.scrollDOM.getBoundingClientRect();
    const content = view.contentDOM.getBoundingClientRect();
    const block = view.lineBlockAtHeight(port.top - view.documentTop);
    const y = Math.max(port.top, view.documentTop + block.top) + 4;
    const pos = view.posAtCoords({ x: Math.max(content.left, port.left) + 4, y }, false);
    return { pos, line: view.defaultLineHeight };
  });
  const placeOf = (pos: number) => page.evaluate((pos) => {
    const view = window.__pretty.view();
    const rect = view.coordsAtPos(pos, 1);
    const port = view.scrollDOM.getBoundingClientRect();
    return rect ? { top: rect.top - port.top, left: rect.left - port.left } : null;
  }, pos);
  // Off, Pretty, Wrap and Off again: every change between line layouts.
  for (const [i, layout] of ["off", "pretty", "wrap", "off"].entries()) {
    const before = await topText();
    const placeBefore = (await placeOf(before.pos))!;
    const start = await page.evaluate(() => {
      const view = window.__pretty.view();
      const rect = view.coordsAtPos(0);
      const port = view.scrollDOM.getBoundingClientRect();
      const visible = Boolean(rect && rect.bottom > port.top && rect.top < port.bottom && rect.right >= port.left && rect.left <= port.right);
      return { visible, scrollTop: view.scrollDOM.scrollTop, scrollLeft: view.scrollDOM.scrollLeft };
    });
    expect(start.visible, `before switch ${i}: the caret is on screen at ${start.scrollTop}, ${start.scrollLeft}`).toBe(false);
    await page.getByTestId(`files-editor-layout-${layout}`).click();
    await expect(page.getByTestId(`files-editor-layout-${layout}`)).toHaveAttribute("aria-pressed", "true");
    let place: { top: number; left: number } | null = null;
    await expect.poll(async () => {
      place = await placeOf(before.pos);
      return place !== null && Math.abs(place.top - placeBefore.top) < 2 * before.line && place.left >= 0 && place.left < 200;
      // Laying out a megabyte and measuring the line it becomes takes seconds on a loaded machine.
    }, { timeout: 15_000 }).toBe(true).catch((error: unknown) => {
      throw new Error(`switch ${i} to ${layout}: pos ${before.pos} was at ${JSON.stringify(placeBefore)}, now ${JSON.stringify(place)}`, { cause: error });
    });
    await settle(page);
  }
});

modelIndependentWebE2e("stays in Pretty and wraps a file it cannot lay out", async ({ page, request }) => {
  const line = `exec ./server ${"--flag value ".repeat(40)}`;
  await openProjectFilesFixture(page, request, {
    prefix: "pretty-display",
    name: "Pretty display",
    seed: (root) => writeFileSync(path.join(root, "serve.sh"), `#!/bin/sh\n${line}\n`),
  });
  await page.getByTestId("files-tree-file").filter({ hasText: "serve.sh" }).click();
  const host = page.getByTestId("files-editor-host");
  await expect(host.locator(".cm-content")).toBeEditable();
  const status = page.locator(".den-files-editor__status").filter({ visible: true });
  const before = await status.innerText();
  await page.getByTestId("files-editor-layout-pretty").click();
  await expect(page.getByTestId("files-editor-layout-pretty")).toHaveAttribute("aria-pressed", "true");
  await expect(host.locator(".cm-content")).toHaveClass(/cm-lineWrapping/);
  await expect(host.locator(".pw-pretty-break")).toHaveCount(0);
  expect(await status.innerText()).toBe(before);
  const rows = await host.locator(".cm-line").nth(1).evaluate((element) =>
    Math.round(element.getBoundingClientRect().height / parseFloat(getComputedStyle(element).lineHeight)));
  expect(rows).toBeGreaterThan(1);
});

modelIndependentWebE2e("lays out to the width the project's formatter declares", async ({ page, request }) => {
  const call = `run(${Array.from({ length: 12 }, (_, i) => `value${i}`).join(", ")});\n`;
  const breaks = () => page.evaluate(() => window.__pretty.breaksIn(0, window.__pretty.view().state.doc.length).length);
  await openPretty(page, request, { name: "plain.ts", text: call });
  await expect.poll(breaks).toBe(0);
  await openPretty(page, request, { name: "narrow.ts", text: call, alongside: { ".prettierrc": "printWidth: 60\n" } });
  await expect.poll(breaks).toBeGreaterThan(0);
});

modelIndependentWebE2e("renders source text at every scroll position", async ({ page, request }) => {
  const nodes: number[] = [];
  for (const items of [ITEMS_200_KB, ITEMS_1_MB]) {
    const { scroller } = await openPretty(page, request, items);
    for (const fraction of [0, 0.5, 1]) {
      await scroller.evaluate((element, fraction) => {
        element.scrollTop = (element.scrollHeight - element.clientHeight) * fraction;
      }, fraction);
      await settle(page);
      await settle(page);
      const mismatches = await page.evaluate(() => {
        const view = window.__pretty.view();
        const doc = view.state.doc;
        const bad: string[] = [];
        for (const line of view.contentDOM.querySelectorAll(".cm-line")) {
          // A long line is drawn as a window; compare from its first text.
          const walker = document.createTreeWalker(line, NodeFilter.SHOW_TEXT);
          let first: Node | null = walker.nextNode();
          while (first && !first.nodeValue) first = walker.nextNode();
          if (!first) continue;
          let text = "";
          for (let node: Node | null = first; node; node = walker.nextNode()) text += node.nodeValue ?? "";
          const from = view.posAtDOM(first, 0);
          if (text.length < 200 || doc.sliceString(from, from + text.length) !== text) bad.push(`${from}:${text.length}`);
        }
        return bad;
      });
      expect(mismatches, `at ${fraction}`).toEqual([]);
    }
    await page.keyboard.press(process.platform === "darwin" ? "Meta+ArrowDown" : "Control+End");
    await expect.poll(() => page.evaluate(() => {
      const view = window.__pretty.view();
      const head = view.state.selection.main.head;
      const rect = view.coordsAtPos(head);
      const port = view.scrollDOM.getBoundingClientRect();
      return head === view.state.doc.length && Boolean(rect && rect.top < port.bottom && rect.bottom > port.top);
    }), { timeout: 5_000 }).toBe(true);
    nodes.push(await page.evaluate(() => window.__pretty.view().contentDOM.querySelectorAll("*").length));
  }
  expect(nodes[1]!).toBeLessThan(nodes[0]! * 2 + 2_000);
});
