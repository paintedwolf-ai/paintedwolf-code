import { createSignal } from "solid-js";
import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  CODE_FOLD_LINE_THRESHOLD,
  CODE_FOLD_VISIBLE_LINES,
  countFenceLines,
  createMarkdownCodeFoldController,
} from "./code-fold.ts";
import { renderMarkdownHtml } from "./markdown-render.ts";
import { MarkdownBody } from "../../components/transcript/MarkdownBody.tsx";
import {
  TranscriptViewportProvider,
  createTranscriptViewportController,
} from "../stream/transcript-viewport.tsx";
import { TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT } from "../transcript/presentation/transcript-disclosure.tsx";
import {
  closeFind,
  findController,
  findNext,
  findPrev,
  openFind,
  registerFindableView,
  resetFindControllerForTests,
  setFindQuery,
  setPrimaryFindableView,
} from "../../find/find-controller.ts";
import { tryConsumeEscapeLadder } from "../../find/escape-ladder.ts";
import { collapsedRevealHostsContaining } from "../../find/find-reveal.ts";
import { scrollportFrameParts } from "../../platform/scrolling/scrollport-frame-dom.ts";
import {
  bindScrollportMotion,
  unbindScrollportMotion,
} from "../../platform/scrolling/scrollport-motion.ts";
import { assertNamedControl } from "../../test/controls.ts";
import { mockScrollerMotion } from "../../test/scroll-mock.ts";
import * as clipboard from "../../utils/clipboard.ts";

function buildLines(count: number, prefix = "line"): string {
  const out: string[] = [];
  for (let i = 1; i <= count; i += 1) {
    out.push(`${prefix}-${i}`);
  }
  return out.join("\n");
}

function buildFencedMarkdown(lineCount: number, lang = "ts", prefix = "line"): string {
  return `\`\`\`${lang}\n${buildLines(lineCount, prefix)}\n\`\`\``;
}

const flushMicrotasks = () => new Promise<void>((resolve) => queueMicrotask(resolve));

describe("code-fold", () => {
  afterEach(() => {
    resetFindControllerForTests();
    vi.restoreAllMocks();
  });

  it("counts lines across 1..500 after removing at most one trailing newline", () => {
    expect(CODE_FOLD_LINE_THRESHOLD).toBe(24);
    expect(CODE_FOLD_VISIBLE_LINES).toBe(12);
    expect(countFenceLines("")).toBe(0);
    expect(countFenceLines("\n")).toBe(0);
    expect(countFenceLines("\r\n")).toBe(0);

    for (let n = 1; n <= 500; n += 1) {
      const body = buildLines(n);
      expect(countFenceLines(body)).toBe(n);
      expect(countFenceLines(`${body}\n`)).toBe(n);
      expect(countFenceLines(`${body}\r\n`)).toBe(n);
      expect(countFenceLines(`${body}\n\n`)).toBe(n + 1);
    }
  });

  it("folds iff line count exceeds 24 across heights 1..500 while keeping DOM text intact", () => {
    const root = document.createElement("div");
    root.className = "markdown-body";
    document.body.appendChild(root);
    const fold = createMarkdownCodeFoldController();

    try {
      for (let n = 1; n <= 500; n += 1) {
        const expectedCode = buildLines(n);
        root.innerHTML = renderMarkdownHtml(buildFencedMarkdown(n));
        fold.sync(root);

        const frame = root.querySelector<HTMLElement>(".markdown-code-scroll");
        const code = root.querySelector<HTMLElement>("pre code");
        expect(frame).not.toBeNull();
        expect(code).not.toBeNull();
        expect(scrollportFrameParts(frame!)).not.toBeNull();

        const codeText = code!.textContent ?? "";
        expect(countFenceLines(codeText)).toBe(n);
        expect(codeText.trimEnd()).toBe(expectedCode);

        const toggle = frame!.querySelector<HTMLButtonElement>(".den-code-fold-toggle");
        const fade = frame!.querySelector<HTMLElement>(".den-code-fold-fade");
        const textNode = code!.firstChild;
        expect(textNode).not.toBeNull();

        if (n <= 24) {
          expect(frame!.dataset.codeFold).toBeUndefined();
          expect(toggle).toBeNull();
          expect(fade).toBeNull();
          expect(collapsedRevealHostsContaining(textNode!)).toHaveLength(0);
        } else {
          expect(frame!.dataset.codeFold).toBe("folded");
          expect(fade?.getAttribute("aria-hidden")).toBe("true");
          expect(toggle).not.toBeNull();
          expect(toggle!.textContent).toBe(`Show all ${n} lines`);
          expect(toggle!.getAttribute("aria-expanded")).toBe("false");
          expect(toggle!.getAttribute("aria-label")).toBe(`Show all ${n} lines`);
          assertNamedControl(toggle!, "button");
          expect(collapsedRevealHostsContaining(textNode!)).toHaveLength(1);
        }
      }

      // Check 24 lines with an explicit single trailing newline inside raw pre/code.
      root.innerHTML = `<div class="den-scrollport markdown-code-scroll" data-den-scrollport="x"><div class="den-scrollport__viewport"><pre class="den-scrollport__content"><code>${buildLines(24)}\n</code></pre></div></div>`;
      fold.sync(root);
      const exact24Frame = root.querySelector<HTMLElement>(".markdown-code-scroll")!;
      expect(exact24Frame.dataset.codeFold).toBeUndefined();
      expect(exact24Frame.querySelector(".den-code-fold-toggle")).toBeNull();
    } finally {
      fold.dispose();
      root.remove();
    }
  });

  it("copies the full 400-line source from a folded fence", async () => {
    const copySpy = vi.spyOn(clipboard, "copyTextToClipboard").mockResolvedValue(undefined);
    const full400 = buildLines(400, "stmt");
    const { container } = render(() => (
      <MarkdownBody source={`\`\`\`ts\n${full400}\n\`\`\``} />
    ));
    await flushMicrotasks();

    const frame = container.querySelector<HTMLElement>(".markdown-code-scroll")!;
    expect(frame.dataset.codeFold).toBe("folded");

    const toggle = frame.querySelector<HTMLButtonElement>("[data-testid=code-block-fold-toggle]")!;
    expect(toggle.textContent).toBe("Show all 400 lines");
    assertNamedControl(toggle, "button");

    const copyBtn = frame.querySelector<HTMLButtonElement>("[data-testid=code-block-copy]")!;
    fireEvent.click(copyBtn);

    expect(copySpy).toHaveBeenCalledTimes(1);
    const copied = copySpy.mock.calls[0]?.[0] ?? "";
    expect(copied.trimEnd()).toBe(full400);
    expect(countFenceLines(copied)).toBe(400);
  });

  it("integrates with FindController leases without mutating user fold state", async () => {
    const codeLines: string[] = [];
    for (let i = 1; i <= 60; i += 1) {
      if (i === 30) codeLines.push("const first = 'needle-token';");
      else if (i === 50) codeLines.push("const second = 'needle-token';");
      else codeLines.push(`const row${i} = ${i};`);
    }
    const markdown = [
      "Intro paragraph before fence.",
      "",
      "```ts",
      codeLines.join("\n"),
      "```",
      "",
      "Outro paragraph with needle-token after fence.",
    ].join("\n");

    const { container, unmount } = render(() => <MarkdownBody source={markdown} />);
    await flushMicrotasks();

    const root = container.querySelector<HTMLElement>(".markdown-body")!;
    const frame = container.querySelector<HTMLElement>(".markdown-code-scroll")!;
    const toggle = frame.querySelector<HTMLButtonElement>("[data-testid=code-block-fold-toggle]")!;

    registerFindableView({
      id: "session-transcript",
      rootEl: () => root,
      scrollMatchIntoView: () => {},
    });
    setPrimaryFindableView("session-transcript");

    // Control copy inside the toggle button is excluded from Find matches.
    openFind();
    setFindQuery("Show all 60");
    expect(findController.matches()).toHaveLength(0);

    // Typing counts folded matches without unfolding the fence.
    setFindQuery("needle-token");
    expect(findController.matches()).toHaveLength(3);
    expect(findController.collapsedCount()).toBe(2);
    expect(frame.dataset.codeFold).toBe("folded");
    expect(toggle.getAttribute("aria-expanded")).toBe("false");

    // First Next reveals match 0 inside the folded fence via lease.
    findNext();
    expect(frame.dataset.codeFold).toBe("open");
    expect(findController.activeIndex()).toBe(0);
    expect(findController.collapsedCount()).toBe(0);

    // Second Next advances to match 1 inside the same fence and retains the lease.
    findNext();
    expect(findController.activeIndex()).toBe(1);
    expect(frame.dataset.codeFold).toBe("open");

    // Third Next moves to the outro match outside the fence, releasing the lease.
    findNext();
    expect(findController.activeIndex()).toBe(2);
    expect(frame.dataset.codeFold).toBe("folded");
    expect(toggle.textContent).toBe("Show all 60 lines");

    // Moving back into the fence re-acquires the lease, and Escape releases it.
    findPrev();
    expect(frame.dataset.codeFold).toBe("open");
    expect(tryConsumeEscapeLadder()).toBe(true);
    expect(findController.isOpen()).toBe(false);
    expect(frame.dataset.codeFold).toBe("folded");

    // Close also restores clipping when active on a folded match.
    openFind();
    setFindQuery("needle-token");
    findNext();
    expect(frame.dataset.codeFold).toBe("open");
    closeFind();
    expect(frame.dataset.codeFold).toBe("folded");

    // When the user explicitly opens the fence, Find leases leave it open after Close.
    fireEvent.click(toggle);
    expect(frame.dataset.codeFold).toBe("open");
    expect(toggle.textContent).toBe("Show less");
    expect(toggle.getAttribute("aria-label")).toBe("Show less (60 lines)");
    expect(toggle.getAttribute("aria-expanded")).toBe("true");

    openFind();
    setFindQuery("needle-token");
    expect(findController.collapsedCount()).toBe(0);
    findNext();
    closeFind();
    expect(frame.dataset.codeFold).toBe("open");
    expect(toggle.textContent).toBe("Show less");

    unmount();
  });

  it("preserves user fold state by fence index across re-renders and cleans up hosts", async () => {
    const [source, setSource] = createSignal(
      `${buildFencedMarkdown(30, "ts", "first")}\n\n${buildFencedMarkdown(40, "ts", "second")}`,
    );

    const { container, unmount } = render(() => <MarkdownBody source={source()} />);
    await flushMicrotasks();

    const initialFrames = container.querySelectorAll<HTMLElement>(".markdown-code-scroll");
    expect(initialFrames).toHaveLength(2);
    expect(initialFrames[0]!.dataset.codeFold).toBe("folded");
    expect(initialFrames[1]!.dataset.codeFold).toBe("folded");

    // User opens fence 0; fence 1 stays folded.
    fireEvent.click(
      initialFrames[0]!.querySelector<HTMLButtonElement>("[data-testid=code-block-fold-toggle]")!,
    );
    expect(initialFrames[0]!.dataset.codeFold).toBe("open");
    expect(initialFrames[1]!.dataset.codeFold).toBe("folded");

    // Streaming re-renders innerHTML with fence 0 grown to 35 lines.
    setSource(
      `${buildFencedMarkdown(35, "ts", "first")}\n\n${buildFencedMarkdown(40, "ts", "second")}`,
    );
    await waitFor(async () => {
      await flushMicrotasks();
      const updatedFrames = container.querySelectorAll<HTMLElement>(".markdown-code-scroll");
      expect(updatedFrames).toHaveLength(2);
      expect(updatedFrames[0]!.dataset.codeFold).toBe("open");
      expect(
        updatedFrames[0]!
          .querySelector<HTMLButtonElement>("[data-testid=code-block-fold-toggle]")
          ?.getAttribute("aria-label"),
      ).toBe("Show less (35 lines)");
      expect(updatedFrames[1]!.dataset.codeFold).toBe("folded");
    });

    // Shrinking fence 1 to <= 24 lines strips fold chrome and unregisters its reveal host.
    setSource(
      `${buildFencedMarkdown(35, "ts", "first")}\n\n${buildFencedMarkdown(20, "ts", "second")}`,
    );
    await waitFor(async () => {
      await flushMicrotasks();
      const shrunkFrames = container.querySelectorAll<HTMLElement>(".markdown-code-scroll");
      expect(shrunkFrames).toHaveLength(2);
      expect(shrunkFrames[0]!.dataset.codeFold).toBe("open");
      expect(shrunkFrames[1]!.dataset.codeFold).toBeUndefined();
      expect(shrunkFrames[1]!.querySelector(".den-code-fold-toggle")).toBeNull();
      const secondTextNode = shrunkFrames[1]!.querySelector("code")!.firstChild!;
      expect(collapsedRevealHostsContaining(secondTextNode)).toHaveLength(0);
    });

    // Fold fence 0 again, capture its code text node, unmount, and verify host cleanup.
    const activeFrame0 = container.querySelectorAll<HTMLElement>(".markdown-code-scroll")[0]!;
    fireEvent.click(
      activeFrame0.querySelector<HTMLButtonElement>("[data-testid=code-block-fold-toggle]")!,
    );
    const firstTextNode = activeFrame0.querySelector("code")!.firstChild!;
    expect(collapsedRevealHostsContaining(firstTextNode)).toHaveLength(1);

    unmount();
    expect(collapsedRevealHostsContaining(firstTextNode)).toHaveLength(0);
  });

  it("preserves the transcript reading anchor when expanding and collapsing a 400-line fence", async () => {
    const viewport = createTranscriptViewportController({
      sessionId: () => "fold-anchor-session",
    });

    let streamEl!: HTMLDivElement;
    let rowEl!: HTMLDivElement;

    const { unmount } = render(() => (
      <TranscriptViewportProvider value={viewport}>
        <div class="den-chat-stream" ref={streamEl}>
          <div style={{ height: "5000px" }} />
          <div
            class="transcript-viewport-row"
            data-index="5"
            data-virtual-start="5000"
            ref={rowEl}
          >
            <MarkdownBody source={buildFencedMarkdown(400, "ts", "tall")} />
          </div>
          <div class="transcript-viewport-row" data-index="6" data-virtual-start="12220">
            <p>Anchored next message</p>
          </div>
        </div>
      </TranscriptViewportProvider>
    ));

    bindScrollportMotion(streamEl, streamEl, streamEl);
    viewport.attachStream(streamEl);
    await flushMicrotasks();

    const frame = rowEl.querySelector<HTMLElement>(".markdown-code-scroll")!;
    const toggle = frame.querySelector<HTMLButtonElement>("[data-testid=code-block-fold-toggle]")!;
    let layoutEvents = 0;
    rowEl.addEventListener(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, () => {
      layoutEvents += 1;
    });

    // Model layout geometry: row starts at document y=5000; folded=240px, open=7200px.
    const rowStart = 5000;
    const frameHeight = () => (frame.dataset.codeFold === "open" ? 7200 : 240);
    Object.defineProperties(streamEl, {
      clientHeight: { configurable: true, get: () => 600 },
      scrollHeight: { configurable: true, get: () => rowStart + frameHeight() + 2000 },
    });
    mockScrollerMotion(streamEl);
    vi.spyOn(streamEl, "getBoundingClientRect").mockImplementation(() =>
      new DOMRect(0, 0, 700, 600),
    );
    vi.spyOn(frame, "getBoundingClientRect").mockImplementation(() =>
      new DOMRect(0, rowStart - streamEl.scrollTop, 700, frameHeight()),
    );
    vi.spyOn(toggle, "getBoundingClientRect").mockImplementation(() =>
      new DOMRect(0, rowStart + frameHeight() - 30 - streamEl.scrollTop, 700, 30),
    );

    try {
      // 1. Expanding while tail-following stops following instead of jumping to line 400.
      streamEl.scrollTop = 4940;
      expect(viewport.following()).toBe(true);
      fireEvent.click(toggle);
      expect(frame.dataset.codeFold).toBe("open");
      expect(viewport.following()).toBe(false);
      expect(streamEl.scrollTop).toBe(4940);
      expect(layoutEvents).toBe(1);

      // 2. Reader scrolls to the bottom of the 400-line fence and clicks "Show less".
      viewport.stopFollowing();
      streamEl.scrollTop = 11900;
      const buttonTopBefore = toggle.getBoundingClientRect().top;
      expect(buttonTopBefore).toBe(270);

      fireEvent.click(toggle);
      expect(frame.dataset.codeFold).toBe("folded");
      expect(layoutEvents).toBe(2);

      // Scroll shifts by -6960 so the collapsed fence bottom and next message stay in place.
      expect(streamEl.scrollTop).toBe(4940);
      expect(toggle.getBoundingClientRect().top).toBe(buttonTopBefore);

      // 3. When the fence top is already visible on screen, collapsing keeps scrollTop steady.
      fireEvent.click(toggle);
      expect(frame.dataset.codeFold).toBe("open");
      streamEl.scrollTop = 4940;
      expect(frame.getBoundingClientRect().top).toBe(60);

      fireEvent.click(toggle);
      expect(frame.dataset.codeFold).toBe("folded");
      expect(streamEl.scrollTop).toBe(4940);
      expect(frame.getBoundingClientRect().top).toBe(60);
    } finally {
      viewport.attachStream(null);
      unbindScrollportMotion(streamEl);
      unmount();
    }
  });
});
